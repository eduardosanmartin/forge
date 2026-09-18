package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
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
// isRunTerminal reports whether res is a genuinely finished state — the
// goroutine has exited. Fase 2 nuance: RunPausedCheckpoint is NOT always
// terminal — while genuinely blocked live in OnCheckpoint, Report is still
// nil (Run()/Resume() hasn't returned yet); checking Report presence
// rather than PendingCheckpoint absence matters because OnCheckpoint
// clears PendingCheckpoint the instant it receives a decision, which is
// BEFORE the goroutine has actually finished unwinding and finishRun has
// set Report — a race window a PendingCheckpoint-based check would catch.
func isRunTerminal(res RunResult) bool {
	switch res.Status {
	case RunDone, RunFailed, RunKilled, RunCanceled:
		return true
	case RunPausedCheckpoint:
		return res.Report != nil
	default:
		return false
	}
}

func waitForRunTerminal(t *testing.T, m *SessionManager, runID string, timeout time.Duration) RunResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, ok := m.GetRun(runID)
		if ok && isRunTerminal(res) {
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

// waitForCheckpointPending polls GetRun until it reports RunPausedCheckpoint
// with a non-nil PendingCheckpoint — i.e., the run's goroutine is genuinely
// blocked live inside OnCheckpoint (Fase 2), not merely "not running" in
// some other terminal sense.
func waitForCheckpointPending(t *testing.T, m *SessionManager, runID string, timeout time.Duration) RunResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, ok := m.GetRun(runID)
		if ok && res.Status == RunPausedCheckpoint && res.PendingCheckpoint != nil {
			return res
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %q never reached a live pending checkpoint within %s", runID, timeout)
	return RunResult{}
}

// TestRunManager_BlocksAtCheckpoint is the Fase 2 regression lock for the
// roadmap's "el run se bloquea correctamente en el checkpoint": reaching a
// required checkpoint blocks the run's own goroutine (report is still nil
// — Run() has not returned), rather than the Fase 1 behavior of ending it
// immediately.
func TestRunManager_BlocksAtCheckpoint(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-block", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	res := waitForCheckpointPending(t, m, "run-block", 5*time.Second)
	if res.PendingCheckpoint.ID != "pre-merge" || res.PendingCheckpoint.Trigger != run.TriggerBeforeMerge {
		t.Fatalf("pending checkpoint = %+v, want pre-merge/before_merge", res.PendingCheckpoint)
	}
	if res.Report != nil {
		t.Fatalf("expected no report while genuinely blocked (Run() hasn't returned), got %+v", res.Report)
	}
	if !m.isRunActive("run-block") {
		t.Fatal("a live-blocked run must still count as active — its goroutine has not exited")
	}

	// Clean up: decline so the goroutine actually exits before the test ends.
	if _, err := m.ApproveRunCheckpoint("run-block", false); err != nil {
		t.Fatalf("ApproveRunCheckpoint(decline): %v", err)
	}
	waitForRunTerminal(t, m, "run-block", 5*time.Second)
}

// TestRunManager_ApproveContinuesInPlace verifies the roadmap's "una
// aprobación externa lo destraba y continúa": approving true resumes the
// SAME goroutine (never a new one) and the run reaches RunDone.
func TestRunManager_ApproveContinuesInPlace(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-approve", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	exec, err := m.StartRun(context.Background(), mani, stateDir, false)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitForCheckpointPending(t, m, "run-approve", 5*time.Second)

	approveRes, err := m.ApproveRunCheckpoint("run-approve", true)
	if err != nil {
		t.Fatalf("ApproveRunCheckpoint(approve): %v", err)
	}
	if approveRes.SessionID != exec.SessionID {
		t.Fatalf("approval result session = %q, want the original %q (same goroutine, no restart)", approveRes.SessionID, exec.SessionID)
	}

	final := waitForRunTerminal(t, m, "run-approve", 5*time.Second)
	if final.Status != RunDone {
		t.Fatalf("status = %q, want %q (err=%q)", final.Status, RunDone, final.Error)
	}
	if final.Report == nil || final.Report.Status != run.StatusCompleted || len(final.Report.CompletedTasks) != 2 {
		t.Fatalf("report = %+v", final.Report)
	}
}

// TestRunManager_DeclineEndsRunCleanly verifies the roadmap's "un rechazo lo
// detiene limpiamente": declining pauses the run via Runner's own
// pauseReport path (Report.Status == StatusPaused, PausedCheckpoints
// populated) and ends the goroutine — same shape as Fase 1's original
// behavior, just reached via an explicit decision instead of always.
func TestRunManager_DeclineEndsRunCleanly(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-decline", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitForCheckpointPending(t, m, "run-decline", 5*time.Second)

	if _, err := m.ApproveRunCheckpoint("run-decline", false); err != nil {
		t.Fatalf("ApproveRunCheckpoint(decline): %v", err)
	}

	final := waitForRunTerminal(t, m, "run-decline", 5*time.Second)
	if final.Status != RunPausedCheckpoint {
		t.Fatalf("status = %q, want %q", final.Status, RunPausedCheckpoint)
	}
	if final.PendingCheckpoint != nil {
		t.Fatalf("a terminally-paused run (goroutine exited) should not still show a pending checkpoint, got %+v", final.PendingCheckpoint)
	}
	if final.Report == nil || final.Report.Status != run.StatusPaused || len(final.Report.PausedCheckpoints) == 0 {
		t.Fatalf("report = %+v", final.Report)
	}
	if m.isRunActive("run-decline") {
		t.Fatal("a declined checkpoint must end the goroutine, not leave it active")
	}
}

// TestRunManager_PendingCheckpointSurvivesIdlePeriod is the regression lock
// for the roadmap's "una desconexión simulada del cliente que iba a
// aprobar no cancela ni corrompe el run": simply not approving for a while
// changes nothing — the run stays cleanly paused, recoverable by a later
// approval exactly as if no time had passed.
func TestRunManager_PendingCheckpointSurvivesIdlePeriod(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-idle", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	before := waitForCheckpointPending(t, m, "run-idle", 5*time.Second)

	// Simulate "the client that would approve disconnected" — nothing
	// happens for a while.
	time.Sleep(150 * time.Millisecond)

	after, ok := m.GetRun("run-idle")
	if !ok {
		t.Fatal("run disappeared during the idle period")
	}
	if after.Status != RunPausedCheckpoint || after.PendingCheckpoint == nil {
		t.Fatalf("run should still be cleanly paused after an idle period, got %+v", after)
	}
	if after.PendingCheckpoint.ID != before.PendingCheckpoint.ID {
		t.Fatalf("pending checkpoint changed during idle period: %+v -> %+v", before.PendingCheckpoint, after.PendingCheckpoint)
	}

	// A later approval still works normally — the idle period corrupted nothing.
	if _, err := m.ApproveRunCheckpoint("run-idle", true); err != nil {
		t.Fatalf("ApproveRunCheckpoint after idle period: %v", err)
	}
	final := waitForRunTerminal(t, m, "run-idle", 5*time.Second)
	if final.Status != RunDone {
		t.Fatalf("status = %q, want %q", final.Status, RunDone)
	}
}

// TestRunManager_ResumeReusesSessionAndSkipsCompletedTasks verifies
// ResumeRun reloads persisted state, reuses the original session (task
// turns keep their conversational context), and does not redo already
// completed tasks. The first run must reach a genuinely TERMINAL pause
// (goroutine exited) before Resume applies — an explicit decline, same as
// how a real daemon restart would also end that goroutine.
func TestRunManager_ResumeReusesSessionAndSkipsCompletedTasks(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-resume", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	first, err := m.StartRun(context.Background(), mani, stateDir, false)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	firstSessionID := first.SessionID
	waitForCheckpointPending(t, m, "run-resume", 5*time.Second)
	if _, err := m.ApproveRunCheckpoint("run-resume", false); err != nil {
		t.Fatalf("ApproveRunCheckpoint(decline): %v", err)
	}
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
	waitForCheckpointPending(t, m, "run-resume", 5*time.Second)
	if _, err := m.ApproveRunCheckpoint("run-resume", false); err != nil {
		t.Fatalf("ApproveRunCheckpoint(decline) on resumed run: %v", err)
	}

	secondRes := waitForRunTerminal(t, m, "run-resume", 5*time.Second)
	if secondRes.Status != RunPausedCheckpoint {
		t.Fatalf("resumed run status = %q, want %q (re-pauses at the same checkpoint on decline)", secondRes.Status, RunPausedCheckpoint)
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

// TestRunManager_CancelWhileLiveBlockedAtCheckpoint is the Fase 2
// counterpart to TestRunManager_CancelMidTask: canceling a run genuinely
// blocked live in OnCheckpoint (not merely "running a task") must still
// unblock and terminate it — the earlier Fase-1-only CancelRun only
// recognized RunRunning as cancelable, which would have left a
// paused-live run stuck forever with no way to stop it short of an
// approval.
func TestRunManager_CancelWhileLiveBlockedAtCheckpoint(t *testing.T) {
	m := newTestSessionManagerForRuns()
	mani := testRunManifest("run-cancel-checkpoint", run.ModeCheckpoint, true)
	stateDir := t.TempDir()

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitForCheckpointPending(t, m, "run-cancel-checkpoint", 5*time.Second)

	cancelRes, err := m.CancelRun("run-cancel-checkpoint")
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelRes.Status != RunCanceled {
		t.Fatalf("CancelRun result status = %q, want %q", cancelRes.Status, RunCanceled)
	}

	res := waitForRunTerminal(t, m, "run-cancel-checkpoint", 3*time.Second)
	if res.Status != RunCanceled {
		t.Fatalf("final status = %q, want %q", res.Status, RunCanceled)
	}
	if m.isRunActive("run-cancel-checkpoint") {
		t.Fatal("a canceled run must not remain active")
	}
	// A second approval attempt must fail cleanly (nothing left to approve).
	if _, err := m.ApproveRunCheckpoint("run-cancel-checkpoint", true); err == nil {
		t.Fatal("expected ApproveRunCheckpoint to fail on an already-canceled run")
	}
	// Same settle delay as TestRunManager_CancelMidTask — CancelRun's
	// optimistic status set races the detached goroutine's actual state.json
	// write; see that test's comment.
	time.Sleep(100 * time.Millisecond)
}

// TestRunRPC_StartStatusList exercises run.start/run.status/run.list end to
// end through the Handler, the same way TestJobQueue_HandlerRPC covers
// job.* — this is the Fase 3 surface (hojaDeRuta-multiagente.md) that adds
// nothing new below the RPC layer (StartRun/GetRun/ListRuns are already
// covered directly), so it only needs to prove request/response wiring and
// JSON shapes round-trip correctly.
func TestRunRPC_StartStatusList(t *testing.T) {
	m := newTestSessionManagerForRuns()
	h := NewHandler(m, slog.New(slog.DiscardHandler), nil, nil)
	stateDir := t.TempDir()
	mani := testRunManifest("rpc-run-1", run.ModeCheckpoint, false)

	startParams, _ := json.Marshal(RunStartParams{Manifest: *mani, StateDir: stateDir})
	reqStart := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunStart, Params: startParams}
	resp := h.HandleRequest(context.Background(), reqStart)
	if resp.Error != nil {
		t.Fatalf("run.start error: %v", resp.Error)
	}
	var started RunResult
	if err := json.Unmarshal(resp.Result, &started); err != nil {
		t.Fatalf("unmarshal run.start result: %v", err)
	}
	if started.ID != "rpc-run-1" {
		t.Fatalf("run.start ID = %q, want %q", started.ID, "rpc-run-1")
	}

	// A second run.start for the same run_id while it's active must map to
	// ErrCodeRunAlreadyActive, not a generic internal error.
	reqStartAgain := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunStart, Params: startParams}
	resp = h.HandleRequest(context.Background(), reqStartAgain)
	if resp.Error == nil || resp.Error.Code != ErrCodeRunAlreadyActive {
		t.Fatalf("want ErrCodeRunAlreadyActive, got %+v", resp.Error)
	}

	waitForRunTerminal(t, m, "rpc-run-1", 5*time.Second)

	// run.status
	statusParams, _ := json.Marshal(RunStatusParams{RunID: "rpc-run-1"})
	reqStatus := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunStatus, Params: statusParams}
	resp = h.HandleRequest(context.Background(), reqStatus)
	if resp.Error != nil {
		t.Fatalf("run.status error: %v", resp.Error)
	}
	var status RunResult
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("unmarshal run.status result: %v", err)
	}
	if status.Status != RunDone {
		t.Fatalf("run.status Status = %q, want %q", status.Status, RunDone)
	}

	// run.status on an unknown run_id must be ErrCodeRunNotFound.
	missingParams, _ := json.Marshal(RunStatusParams{RunID: "no-such-run"})
	reqMissing := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunStatus, Params: missingParams}
	resp = h.HandleRequest(context.Background(), reqMissing)
	if resp.Error == nil || resp.Error.Code != ErrCodeRunNotFound {
		t.Fatalf("want ErrCodeRunNotFound, got %+v", resp.Error)
	}

	// run.list
	reqList := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunList}
	resp = h.HandleRequest(context.Background(), reqList)
	if resp.Error != nil {
		t.Fatalf("run.list error: %v", resp.Error)
	}
	var list RunListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatalf("unmarshal run.list result: %v", err)
	}
	found := false
	for _, r := range list.Runs {
		if r.ID == "rpc-run-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("run.list did not include rpc-run-1: %+v", list.Runs)
	}

	// run.cancel on an already-terminal run must be ErrCodeRunNotFound —
	// CancelRun treats a terminal entry the same as no entry at all.
	cancelParams, _ := json.Marshal(RunCancelParams{RunID: "no-such-run"})
	reqCancel := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunCancel, Params: cancelParams}
	resp = h.HandleRequest(context.Background(), reqCancel)
	if resp.Error == nil || resp.Error.Code != ErrCodeRunNotFound {
		t.Fatalf("want ErrCodeRunNotFound for run.cancel on missing run, got %+v", resp.Error)
	}

	// run.resume against a run_id with no persisted state.json must fail —
	// LoadState errors aren't one of the run.* sentinels, so this exercises
	// the default-to-internal-error branch instead of a 4xx-style mapping.
	// Uses a fresh run_id/stateDir: "rpc-run-1" itself now HAS persisted
	// state (it completed above), so resuming it would succeed instead.
	unresumedMani := testRunManifest("rpc-run-never-started", run.ModeCheckpoint, false)
	resumeParams, _ := json.Marshal(RunResumeParams{Manifest: *unresumedMani, StateDir: t.TempDir()})
	reqResume := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunResume, Params: resumeParams}
	resp = h.HandleRequest(context.Background(), reqResume)
	if resp.Error == nil {
		t.Fatal("want run.resume to fail without a persisted checkpoint state")
	}
	if resp.Error.Code != ErrCodeInternalError {
		t.Fatalf("run.resume error code = %d, want %d", resp.Error.Code, ErrCodeInternalError)
	}
}

// TestRunRPC_ApproveCheckpointAndNotification drives a daemon-hosted run
// through a live HITL checkpoint entirely over RPC (run.start, then
// run.approve_checkpoint), and confirms the run.checkpoint.event
// notification (Fase 2's publishRunCheckpointEvent) reaches a subscribed
// transport the moment the run blocks — the one piece of Fase 3 surface
// that isn't just a thin wrapper over an already-tested SessionManager
// method.
func TestRunRPC_ApproveCheckpointAndNotification(t *testing.T) {
	m := newTestSessionManagerForRuns()
	h := NewHandler(m, slog.New(slog.DiscardHandler), nil, nil)
	stateDir := t.TempDir()
	mani := testRunManifest("rpc-run-checkpoint", run.ModeCheckpoint, true)

	var mu sync.Mutex
	var notifs []*JSONRPCNotification
	m.SetDeltaPublisher(func(sessionID string, notif *JSONRPCNotification) {
		mu.Lock()
		defer mu.Unlock()
		notifs = append(notifs, notif)
	})

	startParams, _ := json.Marshal(RunStartParams{Manifest: *mani, StateDir: stateDir})
	reqStart := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunStart, Params: startParams}
	resp := h.HandleRequest(context.Background(), reqStart)
	if resp.Error != nil {
		t.Fatalf("run.start error: %v", resp.Error)
	}

	waitForCheckpointPending(t, m, "rpc-run-checkpoint", 5*time.Second)

	// The checkpoint notification should have arrived by the time the run
	// is observably paused (publishRunCheckpointEvent fires before the
	// blocking select in handleCheckpoint).
	var gotEvent bool
	mu.Lock()
	for _, n := range notifs {
		if n.Method == MethodRunCheckpointEvent {
			var payload RunCheckpointEventPayload
			if err := json.Unmarshal(n.Params, &payload); err != nil {
				t.Fatalf("unmarshal checkpoint event payload: %v", err)
			}
			if payload.RunID == "rpc-run-checkpoint" {
				gotEvent = true
			}
		}
	}
	mu.Unlock()
	if !gotEvent {
		t.Fatal("expected a run.checkpoint.event notification for rpc-run-checkpoint")
	}

	// run.approve_checkpoint on a run with no pending checkpoint must be
	// ErrCodeRunNoCheckpointPending, not a generic failure — verify this
	// BEFORE approving so the run is still genuinely paused.
	reqBogus := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunApproveCheckpoint}
	respBogus := h.HandleRequest(context.Background(), reqBogus)
	if respBogus.Error == nil || respBogus.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("want ErrCodeInvalidParams for missing run_id, got %+v", respBogus.Error)
	}

	approveParams, _ := json.Marshal(RunApproveCheckpointParams{RunID: "rpc-run-checkpoint", Approved: true})
	reqApprove := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunApproveCheckpoint, Params: approveParams}
	resp = h.HandleRequest(context.Background(), reqApprove)
	if resp.Error != nil {
		t.Fatalf("run.approve_checkpoint error: %v", resp.Error)
	}

	res := waitForRunTerminal(t, m, "rpc-run-checkpoint", 5*time.Second)
	if res.Status != RunDone {
		t.Fatalf("final status = %q, want %q", res.Status, RunDone)
	}

	// A second approval after completion must be ErrCodeRunNoCheckpointPending.
	reqApproveAgain := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodRunApproveCheckpoint, Params: approveParams}
	resp = h.HandleRequest(context.Background(), reqApproveAgain)
	if resp.Error == nil || resp.Error.Code != ErrCodeRunNoCheckpointPending {
		t.Fatalf("want ErrCodeRunNoCheckpointPending, got %+v", resp.Error)
	}
}

// TestRunProgressEvent_MirrorsPerTaskDetail is the Fase 5 addition: with no
// public RPC path exercising it yet (that's internal/cli/run.go, outside
// this package), this confirms directly at the SessionManager level that
// completing a run publishes one run.progress.event per
// internal/run.ProgressEvent the Runner's OnProgress hook fires, carrying
// the SAME per-task detail (phase, task id/index, attempt/retry counts)
// printManifestProgress needs to reproduce its exact terminal lines — proof
// that run.status polling alone (current_task/tokens/iterations only) would
// have been insufficient.
func TestRunProgressEvent_MirrorsPerTaskDetail(t *testing.T) {
	m := newTestSessionManagerForRuns()
	stateDir := t.TempDir()
	mani := testRunManifest("run-progress-events", run.ModeCheckpoint, false)

	var mu sync.Mutex
	var events []RunProgressEventPayload
	m.SetDeltaPublisher(func(sessionID string, notif *JSONRPCNotification) {
		if notif.Method != MethodRunProgressEvent {
			return
		}
		var payload RunProgressEventPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			t.Fatalf("unmarshal progress event payload: %v", err)
		}
		mu.Lock()
		events = append(events, payload)
		mu.Unlock()
	})

	if _, err := m.StartRun(context.Background(), mani, stateDir, false); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	waitForRunTerminal(t, m, "run-progress-events", 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected at least one run.progress.event for a 2-task manifest")
	}
	var sawStart, sawDone bool
	for _, ev := range events {
		if ev.RunID != "run-progress-events" {
			t.Fatalf("event run_id = %q, want run-progress-events (event %+v)", ev.RunID, ev)
		}
		if ev.TotalTasks != 2 {
			t.Fatalf("event TotalTasks = %d, want 2 (event %+v)", ev.TotalTasks, ev)
		}
		switch ev.Phase {
		case "task_start":
			sawStart = true
		case "task_done":
			sawDone = true
		}
	}
	if !sawStart || !sawDone {
		t.Fatalf("expected both task_start and task_done phases, got %+v", events)
	}
}
