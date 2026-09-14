package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

func TestBuildGitHubArgsIssueList(t *testing.T) {
	args, err := buildGitHubArgs("issue-list", map[string]any{})
	if err != nil {
		t.Fatalf("buildGitHubArgs: %v", err)
	}
	want := []string{"issue", "list", "--json", githubIssueListFields}
	if !equalStrings(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestBuildGitHubArgsIssueListWithFilters(t *testing.T) {
	args, err := buildGitHubArgs("issue-list", map[string]any{
		"limit": 5.0, "state": "closed", "search": "label:bug", "repo": "owner/repo",
	})
	if err != nil {
		t.Fatalf("buildGitHubArgs: %v", err)
	}
	want := []string{"issue", "list", "--json", githubIssueListFields, "--limit", "5", "--state", "closed", "--search", "label:bug", "--repo", "owner/repo"}
	if !equalStrings(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestBuildGitHubArgsIssueView(t *testing.T) {
	args, err := buildGitHubArgs("issue-view", map[string]any{"number": 42.0})
	if err != nil {
		t.Fatalf("buildGitHubArgs: %v", err)
	}
	want := []string{"issue", "view", "42", "--json", githubIssueViewFields}
	if !equalStrings(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestBuildGitHubArgsIssueViewMissingNumber(t *testing.T) {
	if _, err := buildGitHubArgs("issue-view", map[string]any{}); err == nil {
		t.Fatal("expected an error when number is missing")
	}
	if _, err := buildGitHubArgs("issue-view", map[string]any{"number": 0.0}); err == nil {
		t.Fatal("expected an error when number is zero")
	}
	if _, err := buildGitHubArgs("issue-view", map[string]any{"number": -1.0}); err == nil {
		t.Fatal("expected an error when number is negative")
	}
}

func TestBuildGitHubArgsPRListAndView(t *testing.T) {
	listArgs, err := buildGitHubArgs("pr-list", map[string]any{})
	if err != nil {
		t.Fatalf("pr-list: %v", err)
	}
	if !equalStrings(listArgs, []string{"pr", "list", "--json", githubPRListFields}) {
		t.Errorf("pr-list args = %v", listArgs)
	}

	viewArgs, err := buildGitHubArgs("pr-view", map[string]any{"number": 7.0})
	if err != nil {
		t.Fatalf("pr-view: %v", err)
	}
	if !equalStrings(viewArgs, []string{"pr", "view", "7", "--json", githubPRViewFields}) {
		t.Errorf("pr-view args = %v", viewArgs)
	}
}

func TestBuildGitHubArgsUnknownSubcommand(t *testing.T) {
	if _, err := buildGitHubArgs("issue-delete", nil); err == nil {
		t.Fatal("expected an error for an unrecognized subcommand — there must be no path to a mutating gh command")
	}
}

func TestGitHubToolSchemaLimitsSubcommandEnum(t *testing.T) {
	tool := newGitHubTool()
	schema := tool.JSONSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	sub, ok := props["subcommand"].(map[string]any)
	if !ok {
		t.Fatal("schema has no subcommand property")
	}
	enum, ok := sub["enum"].([]string)
	if !ok || len(enum) != 4 {
		t.Fatalf("expected a 4-value enum, got %v", sub["enum"])
	}
}

func TestBuildPermsRequestGitHub(t *testing.T) {
	req, err := BuildPermsRequest("github", map[string]any{"subcommand": "pr-list"})
	if err != nil {
		t.Fatalf("BuildPermsRequest: %v", err)
	}
	if req.Kind != perms.KindGitHub {
		t.Errorf("Kind = %v, want KindGitHub", req.Kind)
	}
	if req.Subcommand != "pr-list" {
		t.Errorf("Subcommand = %q, want pr-list", req.Subcommand)
	}

	if _, err := BuildPermsRequest("github", map[string]any{}); err == nil {
		t.Fatal("expected an error when subcommand is missing")
	}
}

// TestGitHubToolExecuteLive is a light smoke test against the real `gh`
// binary, skipped whenever it isn't on PATH (this repo doesn't otherwise
// depend on gh being installed). It only checks that Execute runs the
// mapped command and returns SOME output/metadata — not that any particular
// repo has issues, since that would make the test depend on live repo
// state.
func TestGitHubToolExecuteLive(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not installed, skipping live smoke test")
	}
	tool := newGitHubTool()
	req := perms.Request{
		Kind:       perms.KindGitHub,
		Subcommand: "issue-list",
		Input:      map[string]any{"repo": "cli/cli", "limit": 1.0},
	}
	res, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute returned a Go error (should surface gh failures via Result instead): %v", err)
	}
	if res.Metadata == nil || res.Metadata["subcommand"] != "issue-list" {
		t.Errorf("expected subcommand metadata, got %+v", res.Metadata)
	}
	// Whatever gh said (JSON on success, an auth/error message on failure),
	// it must not be empty and must not silently swallow a failure exit code
	// as if it were success.
	if strings.TrimSpace(res.Content) == "" {
		t.Error("expected non-empty output")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
