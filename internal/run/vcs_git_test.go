package run

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
)

// realGit runs actual git in dir — validates the exact commands branch
// isolation issues (pathspecs, switch -c, commit, merge --no-ff).
func realGit(dir string) GitRunner {
	return func(ctx context.Context, sub string, args []string) (string, int, string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{sub}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode(), "", nil
		}
		return string(out), 0, "", err
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBranchIsolationAgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitOut(t, dir, "init", "-b", "main")
	gitOut(t, dir, "config", "user.email", "t@forge.local")
	gitOut(t, dir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-m", "base")
	// forge's own state must neither block the clean check nor be committed.
	if err := os.MkdirAll(filepath.Join(dir, ".forge", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "runs", "x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := testManifest(ModeCheckpoint)
	m.Git.MergeToBase = "auto_if_all_hitl_passed"
	n := 0
	exec := func(_ context.Context, task Task) (ExecResult, error) {
		n++
		return ExecResult{Tokens: 1, Iterations: 1}, os.WriteFile(filepath.Join(dir, task.ID+".txt"), []byte(task.Goal), 0o644)
	}
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: exec, RunGit: realGit(dir), OnCheckpoint: approveAll}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.TaskCommits) != 2 || rep.MergeCommit == "" {
		t.Fatalf("report = %+v, want 2 task commits and a merge", rep)
	}
	if cur := gitOut(t, dir, "branch", "--show-current"); cur != "main" {
		t.Fatalf("workspace left on %q, want main after the merge", cur)
	}
	log := gitOut(t, dir, "log", "--format=%s", "run/test-run")
	if !strings.Contains(log, "forge(test-run): t1") || !strings.Contains(log, "forge(test-run): t2") {
		t.Fatalf("work branch log lacks one commit per task:\n%s", log)
	}
	if parents := strings.Fields(gitOut(t, dir, "log", "-1", "--format=%P", "main")); len(parents) != 2 {
		t.Fatalf("main HEAD has %d parents, want a --no-ff merge commit", len(parents))
	}
	if files := gitOut(t, dir, "ls-files"); strings.Contains(files, ".forge") {
		t.Fatalf("forge state was committed:\n%s", files)
	}
}
