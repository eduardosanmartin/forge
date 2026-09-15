package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunResumeRequiresManifestFlag(t *testing.T) {
	err := execRoot(t, "run", "--resume", "hello world")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UsageError, got %v", err)
	}
	if !strings.Contains(ue.Error(), "--resume requires --manifest") {
		t.Errorf("usage message should name the missing --manifest flag, got %v", ue)
	}
}

func writeTestManifest(t *testing.T, mode string) string {
	t.Helper()
	manifest := `{
  "run_id": "resume-test-run",
  "mode": "` + mode + `",
  "goal": "test goal",
  "spec": "SPEC stub",
  "budget": {"max_wall_clock": "30m", "max_tokens": 50000, "max_iterations": 30, "max_retries_per_task": 3},
  "git": {"isolation": "branch", "base_branch": "main", "work_branch": "run/resume-test-run", "commit_per_task": true, "merge_to_base": "manual"},
  "hitl": {"checkpoints": [{"id": "pre-merge", "trigger": "before_merge", "required": true}]},
  "tasks": [{"id": "t1", "goal": "task one"}, {"id": "t2", "goal": "task two"}]
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestRunResumeRejectsDryRunManifest(t *testing.T) {
	path := writeTestManifest(t, "dry_run")
	err := execRoot(t, "run", "--manifest", path, "--resume")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UsageError, got %v", err)
	}
	if !strings.Contains(ue.Error(), "dry_run") {
		t.Errorf("usage message should mention dry_run, got %v", ue)
	}
}

func TestRunResumeWithoutPriorStateFailsClearly(t *testing.T) {
	path := writeTestManifest(t, "checkpoint")
	err := execRoot(t, "run", "--manifest", path, "--resume", "--state-dir", t.TempDir())
	if err == nil {
		t.Fatal("expected an error resuming a run with no persisted state and no daemon")
	}
	if !strings.Contains(err.Error(), "load previous state") {
		t.Errorf("expected the resume-specific state-load error, got %v", err)
	}
}

func TestRunCommandRegistersResumeFlag(t *testing.T) {
	cmd := newRunCommand()
	f := cmd.Flags().Lookup("resume")
	if f == nil {
		t.Fatal("run command missing --resume flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--resume default = %q, want false", f.DefValue)
	}
}
