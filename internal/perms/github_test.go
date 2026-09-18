package perms

import (
	"strings"
	"testing"
)

func TestGitHubAllowListDenyByDefault(t *testing.T) {
	eng, _ := newTestEngine(t, nil) // no GitHub.Allow configured
	d := eng.Check(Request{Kind: KindGitHub, Subcommand: "issue-list"})
	if d.Allowed {
		t.Fatalf("expected deny by default, got %+v", d)
	}
	if !strings.HasPrefix(d.Rule, "default-deny:") {
		t.Errorf("rule = %q, want default-deny prefix", d.Rule)
	}
}

func TestGitHubAllowListPermitsListedSubcommand(t *testing.T) {
	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) {
		p.GitHub = GitHubPermissions{Allow: []string{"issue-list", "pr-view"}}
	})

	allowed := eng.Check(Request{Kind: KindGitHub, Subcommand: "issue-list"})
	if !allowed.Allowed || allowed.Rule != "github:issue-list" {
		t.Errorf("issue-list: got %+v", allowed)
	}

	stillDenied := eng.Check(Request{Kind: KindGitHub, Subcommand: "issue-view"})
	if stillDenied.Allowed {
		t.Errorf("issue-view should stay denied when only issue-list/pr-view are allowed, got %+v", stillDenied)
	}
}

func TestGitHubMalformedRequestEmptySubcommand(t *testing.T) {
	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) {
		p.GitHub = GitHubPermissions{Allow: []string{"issue-list"}}
	})
	d := eng.Check(Request{Kind: KindGitHub, Subcommand: ""})
	if d.Allowed || d.Rule != "malformed-request" {
		t.Errorf("empty subcommand: got %+v, want denied malformed-request", d)
	}
}

func TestValidateGitHubAllowListRejectsUnknownSubcommand(t *testing.T) {
	_, err := newTestEngineErr(t, func(p *PermissionsPolicy) {
		p.GitHub = GitHubPermissions{Allow: []string{"issue-delete"}}
	})
	if err == nil {
		t.Fatal("expected an error for an unrecognized github subcommand")
	}
	if !strings.Contains(err.Error(), "issue-delete") {
		t.Errorf("error should name the bad entry, got %v", err)
	}
}

// newTestEngineErr mirrors newTestEngine but returns the construction error
// instead of failing the test, for cases that are expected to fail New.
func newTestEngineErr(t *testing.T, f func(*PermissionsPolicy)) (*Engine, error) {
	t.Helper()
	root := testWorkspaceRoot(t)
	policy := PermissionsPolicy{
		FS:    FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: ShellPermissions{Allow: []string{}},
		Git:   GitPermissions{Allow: []string{"status"}},
	}
	if f != nil {
		f(&policy)
	}
	return New(policy, root, nil)
}
