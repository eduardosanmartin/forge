package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/run"
)

func writeRunState(t *testing.T, dir string, st run.RunState) {
	t.Helper()
	runDir := filepath.Join(dir, ".forge", "runs", st.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// RF-11.8 regression: after a daemon restart, runs it was responsible for
// vanished from run.list; they could only be resumed by resending the
// manifest by hand.
func TestRecoverRunsRegistersInterruptedAndPausedOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeRunState(t, dir, run.RunState{RunID: "r-running", Status: run.StatusRunning, StartedAt: now, UpdatedAt: now, CurrentTaskID: "t2"})
	writeRunState(t, dir, run.RunState{RunID: "r-paused", Status: run.StatusPaused, StartedAt: now, UpdatedAt: now,
		PausedCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true}})
	writeRunState(t, dir, run.RunState{RunID: "r-done", Status: run.StatusCompleted, StartedAt: now, UpdatedAt: now})
	writeRunState(t, dir, run.RunState{RunID: "r-killed", Status: run.StatusKilled, StartedAt: now, UpdatedAt: now})

	m := newTestSessionManagerForRuns()
	n, err := m.RecoverRuns(dir)
	if err != nil || n != 2 {
		t.Fatalf("RecoverRuns = %d, %v; want 2 (running + paused only)", n, err)
	}
	if res, ok := m.GetRun("r-running"); !ok || res.Status != RunInterrupted || res.CurrentTask != "t2" {
		t.Fatalf("r-running = %+v (found %v), want interrupted at t2", res, ok)
	}
	if res, ok := m.GetRun("r-paused"); !ok || res.Status != RunPausedRecovered || res.PendingCheckpoint == nil {
		t.Fatalf("r-paused = %+v (found %v), want paused_recovered with its checkpoint", res, ok)
	}
	if _, ok := m.GetRun("r-done"); ok {
		t.Fatal("terminal runs must not be registered")
	}
	if m.isRunActive("r-paused") || m.isRunActive("r-running") {
		t.Fatal("recovered runs have no goroutine and must not block a fresh start/resume")
	}
	// Idempotent: a second scan doesn't duplicate or overwrite.
	if n, _ := m.RecoverRuns(dir); n != 0 {
		t.Fatalf("second scan recovered %d, want 0", n)
	}
}

func TestRecoveredRunDeclineCancelsAndMissingManifestIsExplained(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeRunState(t, dir, run.RunState{RunID: "a", Status: run.StatusPaused, StartedAt: now, UpdatedAt: now})
	writeRunState(t, dir, run.RunState{RunID: "b", Status: run.StatusPaused, StartedAt: now, UpdatedAt: now})
	m := newTestSessionManagerForRuns()
	if _, err := m.RecoverRuns(dir); err != nil {
		t.Fatal(err)
	}
	res, err := m.ApproveRunCheckpoint("a", false)
	if err != nil || res.Status != RunCanceled {
		t.Fatalf("decline = %+v, %v; want canceled", res, err)
	}
	if _, err := m.ApproveRunCheckpoint("b", true); err == nil {
		t.Fatal("approving a recovered run without a persisted manifest must explain it can't resume")
	}
}

func TestCancelRecoveredRun(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeRunState(t, dir, run.RunState{RunID: "c", Status: run.StatusRunning, StartedAt: now, UpdatedAt: now})
	m := newTestSessionManagerForRuns()
	if _, err := m.RecoverRuns(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelRun("c"); err != nil {
		t.Fatalf("CancelRun on a recovered run: %v", err)
	}
	if res, _ := m.GetRun("c"); res.Status != RunCanceled {
		t.Fatalf("status = %s, want canceled", res.Status)
	}
}

// RF-7.3: the GUI shows the diff of each task's commit.
func TestTaskDiffFromPersistedCommits(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeRunState(t, dir, run.RunState{RunID: "d", Status: run.StatusCompleted, StartedAt: now, UpdatedAt: now,
		TaskCommits: map[string]string{"t1": "0123456789abcdef"}})
	m := newTestSessionManagerForRuns()
	res, err := m.TaskDiff(context.Background(), "d", "t1", dir)
	if err != nil {
		t.Fatalf("TaskDiff: %v", err)
	}
	if res.Commit != "0123456789abcdef" || !strings.Contains(res.Diff, "+added line") {
		t.Fatalf("diff = %+v", res)
	}
	if _, err := m.TaskDiff(context.Background(), "d", "t2", dir); err == nil || !strings.Contains(err.Error(), "no commit") {
		t.Fatalf("a task without a commit must explain why, got %v", err)
	}
}
