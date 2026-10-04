package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/repomap"
)

func askRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	eng, err := perms.New(perms.PermissionsPolicy{Shell: perms.ShellPermissions{Ask: []string{"go"}}}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewDefaultRegistry(eng, dir, nil)
}

func runGo(t *testing.T, r *Registry, session string) Result {
	t.Helper()
	res, err := r.Execute(WithSessionID(context.Background(), session), "shell_exec", map[string]any{"command": "go", "args": []any{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAskWithoutAskerDenies(t *testing.T) {
	res := runGo(t, askRegistry(t), "s1")
	if !strings.HasPrefix(res.Content, "DENIED") || !strings.Contains(res.Content, "no client available") {
		t.Fatalf("got %q", res.Content)
	}
}

func TestAskOnceSessionAndDeny(t *testing.T) {
	r := askRegistry(t)
	var asked int
	answer := AskAllowOnce
	r.SetAsker(func(_ context.Context, req AskRequest) AskDecision {
		asked++
		if req.Rule != "ask:shell.exec:go" || req.Summary != "go version" || req.SessionID == "" {
			t.Errorf("unexpected ask request %+v", req)
		}
		return answer
	})
	if res := runGo(t, r, "s1"); strings.HasPrefix(res.Content, "DENIED") {
		t.Fatalf("allow_once should run: %q", res.Content)
	}
	runGo(t, r, "s1")
	if asked != 2 {
		t.Fatalf("allow_once must not be remembered: asked %d times", asked)
	}

	answer = AskAllowSession
	runGo(t, r, "s1")
	runGo(t, r, "s1")
	if asked != 3 {
		t.Fatalf("allow_session must be remembered for the session: asked %d times", asked)
	}
	answer = AskDeny
	if res := runGo(t, r, "s2"); !strings.Contains(res.Content, "not approved") {
		t.Fatalf("another session must be asked again and honor deny: %q", res.Content)
	}
}

// A "no" (or no answer) covers that one call. The model used to read
// "DENIED ... (denied by the user)" as a standing ban and stop calling the
// tool even when the user asked again (seen with nemotron-3.5-lightning,
// 2026-10-04); the result now says the denial is scoped to the call, and
// the next call asks again.
func TestAskDenialIsScopedToTheCall(t *testing.T) {
	r := askRegistry(t)
	asked := 0
	r.SetAsker(func(context.Context, AskRequest) AskDecision { asked++; return AskDeny })
	res := runGo(t, r, "s1")
	for _, want := range []string{"DENIED: ask:shell.exec:go", "this call only", "do not retry it in this turn", "asked again"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("denial %q missing %q", res.Content, want)
		}
	}
	if rule, _ := res.Metadata["rule"].(string); strings.Contains(rule, "retry") {
		t.Errorf("audit rule should stay concise, got %q", rule)
	}
	runGo(t, r, "s1")
	if asked != 2 {
		t.Fatalf("a denial must not be remembered: asked %d times", asked)
	}
}

// Without a client to answer, retrying later cannot help, so the result
// does not invite it.
func TestAskWithoutAskerDoesNotInviteRetry(t *testing.T) {
	res := runGo(t, askRegistry(t), "s1")
	if strings.Contains(res.Content, "asked again") {
		t.Fatalf("no-client denial invites a retry: %q", res.Content)
	}
}

// F4: the snapshot hook runs before file-changing tools only, and only
// once the permission check allowed the call.
func TestBeforeMutateHook(t *testing.T) {
	dir := t.TempDir()
	eng, err := perms.New(perms.PermissionsPolicy{FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}}}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewDefaultRegistry(eng, dir, nil)
	var seen []string
	r.SetBeforeMutate(func(ctx context.Context, tool string) { seen = append(seen, tool+"@"+TurnIDFromContext(ctx)) })
	ctx := WithTurnID(context.Background(), "turn-7")
	file := dir + "/a.txt"
	if _, err := r.Execute(ctx, "fs_write", map[string]any{"path": file, "content": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(ctx, "fs_read", map[string]any{"path": file}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(ctx, "shell_exec", map[string]any{"command": "go"}); err != nil { // denied: no shell allow
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "fs_write@turn-7" {
		t.Fatalf("hook calls = %v, want only the allowed fs_write", seen)
	}
}

func TestCodeSymbolsTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\n// Open opens.\nfunc Open(p string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, _ := perms.New(perms.PermissionsPolicy{FS: perms.FSPermissions{Read: []string{"./**"}}}, dir, nil)
	r := New(eng, dir, nil)
	r.Register(NewCodeSymbolsTool(repomap.New(dir)))
	// No os.Chdir: the permission check must not depend on the process cwd
	// (CI macOS, 2026-10-03: /var vs /private/var denied it).
	res, err := r.Execute(context.Background(), "code_symbols", map[string]any{"query": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "a.go:4  func  func Open(p string) error") {
		t.Fatalf("got %q", res.Content)
	}
}

// N1: a mutation guard refusal denies the call before it runs (and before
// the snapshot hook).
func TestMutationGuardDeniesBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	eng, _ := perms.New(perms.PermissionsPolicy{FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}}}, dir, nil)
	r := NewDefaultRegistry(eng, dir, nil)
	r.SetMutationGuard(func(ctx context.Context, tool string) error {
		return errors.New("workspace held by isolated run run-X")
	})
	file := filepath.Join(dir, "x.txt")
	res, err := r.Execute(context.Background(), "fs_write", map[string]any{"path": file, "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Content, "DENIED") || !strings.Contains(res.Content, "run-X") {
		t.Fatalf("got %q", res.Content)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("the write must not have happened")
	}
	if res, _ := r.Execute(context.Background(), "fs_read", map[string]any{"path": file}); strings.Contains(res.Content, "run-X") {
		t.Fatal("reads are not guarded")
	}
}
