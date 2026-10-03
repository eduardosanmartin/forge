package tools

import (
	"context"
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
	if res := runGo(t, r, "s2"); !strings.Contains(res.Content, "denied by the user") {
		t.Fatalf("another session must be asked again and honor deny: %q", res.Content)
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
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(dir) // fs.read paths resolve against the process cwd
	res, err := r.Execute(context.Background(), "code_symbols", map[string]any{"query": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "a.go:4  func  func Open(p string) error") {
		t.Fatalf("got %q", res.Content)
	}
}
