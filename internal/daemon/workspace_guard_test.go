package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/run"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// N1 (review 2026-10-03): branch isolation works on THE workspace's single
// checkout, so two isolated runs at once switch branches under each other
// and mix their commits. Only one may hold the workspace at a time.
func TestIsolatedRunsAreExclusivePerWorkspace(t *testing.T) {
	m := newTestSessionManagerForRuns()
	dir := t.TempDir()
	if _, err := m.StartRun(context.Background(), testRunManifest("run-A", "checkpoint", true), dir, false); err != nil {
		t.Fatal(err)
	}
	waitForCheckpointPending(t, m, "run-A", 5*time.Second)

	_, err := m.StartRun(context.Background(), testRunManifest("run-B", "checkpoint", true), dir, false)
	if !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("second isolated run while run-A holds the workspace: err=%v, want ErrWorkspaceBusy", err)
	}

	// Declining the checkpoint PAUSES run-A: its branch is still checked
	// out, so the workspace stays held.
	m.ApproveRunCheckpoint("run-A", false)
	waitForRunTerminal(t, m, "run-A", 5*time.Second)
	if _, err := m.StartRun(context.Background(), testRunManifest("run-B", "checkpoint", true), dir, false); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("run-A is paused on its branch: run-B must still be refused, got %v", err)
	}

	// Canceling run-A frees the workspace.
	if _, err := m.CancelRun("run-A"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.mutationGuardHolder() != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.StartRun(context.Background(), testRunManifest("run-B", "checkpoint", true), dir, false); err != nil {
		t.Fatalf("after run-A was canceled, run-B must start: %v", err)
	}
	waitForCheckpointPending(t, m, "run-B", 5*time.Second)
	m.ApproveRunCheckpoint("run-B", false)
	waitForRunTerminal(t, m, "run-B", 5*time.Second)
}

// N1, second half: while an isolated run holds the workspace, writes from
// any OTHER session (an interactive chat) would land in the run's next
// task commit. They are refused; the run's own session (and the sessions
// branched from it, e.g. subagents) keep working.
func TestWorkspaceGuardDuringIsolatedRun(t *testing.T) {
	m := newTestSessionManagerForRuns()
	ctx := context.Background()
	runSess, _ := m.CreateSession(ctx, nil)
	other, _ := m.CreateSession(ctx, nil)
	child, _ := m.CreateSession(ctx, map[string]any{"branch_root": runSess.ID, "branch_parent": runSess.ID})

	guard := m.mutationGuard()
	if err := guard(tools.WithSessionID(ctx, other.ID), "fs_write"); err != nil {
		t.Fatalf("no isolated run active: writes must be allowed, got %v", err)
	}

	release, err := m.holdWorkspace("run-X", runSess.ID, "forge/run/run-X")
	if err != nil {
		t.Fatal(err)
	}
	if err := guard(tools.WithSessionID(ctx, other.ID), "fs_write"); err == nil {
		t.Fatal("another session's write during an isolated run must be refused")
	}
	if err := guard(ctx, "shell_exec"); err == nil {
		t.Fatal("a write with no session must be refused while the workspace is held")
	}
	for _, s := range []string{runSess.ID, child.ID} {
		if err := guard(tools.WithSessionID(ctx, s), "fs_write"); err != nil {
			t.Fatalf("session %s belongs to the run and must keep working: %v", s, err)
		}
	}
	release()
	if err := guard(tools.WithSessionID(ctx, other.ID), "fs_write"); err != nil {
		t.Fatalf("after release writes must be allowed again: %v", err)
	}
}

// A run rediscovered after a restart whose branch is still checked out
// (not merged) keeps the workspace held until resumed or canceled.
func TestRecoveredIsolatedRunHoldsWorkspace(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeRunState(t, dir, run.RunState{RunID: "r1", Status: run.StatusPaused, StartedAt: now, UpdatedAt: now, SessionID: "s1", WorkBranch: "forge/run/r1"})
	m := newTestSessionManagerForRuns()
	if _, err := m.RecoverRuns(dir); err != nil {
		t.Fatal(err)
	}
	if got := m.mutationGuardHolder(); got != "r1" {
		t.Fatalf("holder = %q, want r1", got)
	}
	if _, err := m.CancelRun("r1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.mutationGuardHolder() != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := m.mutationGuardHolder(); got != "" {
		t.Fatalf("canceling the recovered run must free the workspace, holder = %q", got)
	}
}
