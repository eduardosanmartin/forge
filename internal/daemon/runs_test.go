package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/run"
)

// testRunManifest mirrors internal/run/runner_test.go's own testManifest —
// same shape (branch isolation, a required before_merge checkpoint, two
// tasks) so these daemon-level tests exercise the real Runner the same way
// its own unit tests already do, just launched through SessionManager
// instead of constructed directly.
func testRunManifest(runID, mode string, withCheckpoint bool) *run.Manifest {
	m := &run.Manifest{
		RunID: runID,
		Mode:  mode,
		Goal:  "implement feature",
		Spec:  "SPEC stub",
		Budget: run.Budget{
			MaxWallClock:      "1h",
			MaxTokens:         100000,
			MaxIterations:     50,
			MaxRetriesPerTask: 2,
		},
		Git:   run.GitConfig{Isolation: "branch", BaseBranch: "main", WorkBranch: "run/" + runID, CommitPerTask: true, MergeToBase: "manual"},
		Tasks: []run.Task{{ID: "t1", Goal: "task one"}, {ID: "t2", Goal: "task two"}},
	}
	if withCheckpoint {
		m.HITL = run.HITLConfig{Checkpoints: []run.Checkpoint{
			{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
		}}
	}
	return m
}

func newTestSessionManagerForRuns() *SessionManager {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	return NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)
}

// waitForRunTerminal polls GetRun until it reaches a non-RunRunning status
// or the timeout elapses.
func waitForRunTerminal(t *testing.T, m *SessionManager, runID string, timeout time.Duration) RunResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, ok := m.GetRun(runID)
		if ok && res.Status != RunRunning {
			return res
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %q did not reach a terminal status within %s", runID, timeout)
	return RunResult{}
}

// TestRunManager_CompletesAllTasks is the Fase 1 happy path: a manifest with
// no required checkpoint runs both tasks to completion via the real
// SessionManager.ExecuteTurn path (testLLMRegistry's mock provider, no real
// LLM), in a daemon-hosted goroutine, and reaches RunDone with a real
// run.Report.
func TestRunManager_CompletesAllTasks(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-complete", run.ModeCheckpoint, false)
	stateDir := t.TempDir()

	exec, err := m.StartRun(context.Background(), mani, stateDir, false)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if exec.ID != "run-complete" {
		t.Fatalf("exec.ID = %q, want run-complete", exec.ID)
	}
	if exec.SessionID == "" {
		t.Fatal("expected a non-dry-run to get a real session id")
	}

	res := waitForRunTerminal(t, m, "run-complete", 5*time.Second)
	if res.Status != RunDone {
		t.Fatalf("status = %q, want %q (err=%q)", res.Status, RunDone, res.Error)
	}
	if res.Report == nil {
		t.Fatal("expected a report on a completed run")
	}
	if res.Report.Status != run.StatusCompleted || len(res.Report.CompletedTasks) != 2 {
		t.Fatalf("report = %+v", res.Report)
	}
}

// TestRunManager_PausesAtCheckpoint verifies a required checkpoint pauses
// the run (Fase 1: OnCheckpoint always declines — see runs.go's doc comment
// — the live-approval channel is Fase 2's job) and that GetRun reflects it.
func TestRunManager_PausesAtCheckpoint(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-pause", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	res := waitForRunTerminal(t, m, "run-pause", 5*time.Second)
	if res.Status != RunPausedCheckpoint {
		t.Fatalf("status = %q, want %q (err=%q)", res.Status, RunPausedCheckpoint, res.Error)
	}
	if res.Report == nil || res.Report.Status != run.StatusPaused {
		t.Fatalf("report = %+v", res.Report)
	}
	if len(res.Report.PausedCheckpoints) == 0 {
		t.Fatalf("expected a paused checkpoint recorded, report = %+v", res.Report)
	}
}

// TestRunManager_ResumeReusesSessionAndSkipsCompletedTasks verifies
// ResumeRun reloads persisted state, reuses the original session (task
// turns keep their conversational context), and does not redo already
// completed tasks.
func TestRunManager_ResumeReusesSessionAndSkipsCompletedTasks(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-resume", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	first, err := m.StartRun(context.Background(), mani, stateDir, false)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	firstSessionID := first.SessionID
	firstRes := waitForRunTerminal(t, m, "run-resume", 5*time.Second)
	if firstRes.Status != RunPausedCheckpoint {
		t.Fatalf("first run status = %q, want %q", firstRes.Status, RunPausedCheckpoint)
	}
	if len(firstRes.Report.CompletedTasks) != 2 {
		t.Fatalf("expected both tasks done before the checkpoint, report = %+v", firstRes.Report)
	}

	// Resuming re-registers the run under the same ID: must not collide
	// with the (now-terminal) first RunExecution.
	second, err := m.ResumeRun(context.Background(), mani, stateDir)
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if second.SessionID != firstSessionID {
		t.Fatalf("resumed session = %q, want original %q (context must carry over)", second.SessionID, firstSessionID)
	}

	secondRes := waitForRunTerminal(t, m, "run-resume", 5*time.Second)
	if secondRes.Status != RunPausedCheckpoint {
		t.Fatalf("resumed run status = %q, want %q (no approval mechanism exists until Fase 2, so it re-pauses at the same checkpoint)", secondRes.Status, RunPausedCheckpoint)
	}
	if len(secondRes.Report.CompletedTasks) != 2 {
		t.Fatalf("resume should not re-run already-completed tasks, report = %+v", secondRes.Report)
	}
}

// TestRunManager_InvalidManifestFails is the "a run that terminates with
// error" case from Fase 1's checklist: Runner.Run's own validateForExecution
// rejects a manifest with an unsafe run_id (path traversal), so the
// goroutine's call() returns a plain error rather than a Report.
func TestRunManager_InvalidManifestFails(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("../escape", run.ModeCheckpoint, false)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	res := waitForRunTerminal(t, m, "../escape", 5*time.Second)
	if res.Status != RunFailed {
		t.Fatalf("status = %q, want %q", res.Status, RunFailed)
	}
	if res.Error == "" {
		t.Fatal("expected a recorded error message")
	}
	if res.Report != nil {
		t.Fatalf("expected no report on a validation failure, got %+v", res.Report)
	}
}

// TestRunManager_CancelMidTask verifies cancellation propagates through the
// real agent loop (context cancellation, same mechanism Job.CancelJob
// already relies on) and the run reaches RunCanceled promptly rather than
// running to completion.
func TestRunManager_CancelMidTask(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	provider := &blockingProvider{
		delay: 2 * time.Second,
		response: llm.ChatResponse{
			ID:    "test-response",
			Model: "test-model",
			Choices: []llm.Choice{{
				Index:        0,
				Message:      llm.Message{Role: "assistant", Content: "Test response"},
				FinishReason: "stop",
			}},
			Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
		},
	}
	llmReg := &blockingRegistry{provider: provider}
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()
	m := NewSessionManager(st, llmReg, toolsReg, emergency, logger, cfg, permsEng, st)

	mani := testRunManifest("run-cancel", run.ModeCheckpoint, false)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Give the goroutine time to register and start the first task's turn
	// (which will now block on the provider's 2s delay).
	time.Sleep(50 * time.Millisecond)
	if res, ok := m.GetRun("run-cancel"); !ok || res.Status != RunRunning {
		t.Fatalf("expected the run to still be mid-task, got %+v (ok=%v)", res, ok)
	}

	cancelRes, err := m.CancelRun("run-cancel")
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelRes.Status != RunCanceled {
		t.Fatalf("CancelRun result status = %q, want %q", cancelRes.Status, RunCanceled)
	}

	res := waitForRunTerminal(t, m, "run-cancel", 3*time.Second)
	if res.Status != RunCanceled {
		t.Fatalf("final status = %q, want %q", res.Status, RunCanceled)
	}
	// CancelRun sets the externally-visible status optimistically (same
	// pattern as Job.CancelJob) — it does not itself wait for the detached
	// goroutine's call() to actually return and finish its state.json
	// write. waitForRunTerminal above sees the optimistic status
	// immediately and doesn't wait for that either. Give the real
	// goroutine a moment to settle before the test ends, or t.TempDir()'s
	// cleanup can race a still-in-flight file write (observed on Windows:
	// "directory is not empty" from a handle not yet closed).
	time.Sleep(100 * time.Millisecond)
}
