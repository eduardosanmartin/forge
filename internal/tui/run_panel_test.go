package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// writeTestManifest writes a minimal valid manifest (same shape as
// internal/daemon/runs_test.go's testRunManifest — required fields only) to
// a temp file and returns its path.
func writeTestManifest(t *testing.T, runID string) string {
	t.Helper()
	mani := run.Manifest{
		RunID: runID,
		Mode:  run.ModeCheckpoint,
		Goal:  "implement feature",
		Spec:  "SPEC stub",
		Budget: run.Budget{
			MaxWallClock:      "1h",
			MaxTokens:         100000,
			MaxIterations:     50,
			MaxRetriesPerTask: 2,
		},
		Git:   run.GitConfig{Isolation: "branch", BaseBranch: "main", WorkBranch: "run/" + runID, CommitPerTask: true, MergeToBase: "manual"},
		Tasks: []run.Task{{ID: "t1", Goal: "task one"}},
	}
	data, err := json.Marshal(mani)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// TestSlashRun_NoArgShowsUsage covers /run with no manifest path.
func TestSlashRun_NoArgShowsUsage(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.input.SetValue("/run")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if !strings.Contains(mm.toast, "usage") {
		t.Fatalf("toast = %q, want usage hint", mm.toast)
	}
}

// TestSlashRun_NotConnectedShowsToast covers /run with no daemon client.
func TestSlashRun_NotConnectedShowsToast(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("/run somewhere.json")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if mm.toast != "not connected" {
		t.Fatalf("toast = %q, want %q", mm.toast, "not connected")
	}
}

// TestSlashRun_InvalidManifestShowsError covers a manifest path that fails
// to parse — cmdRunStart must surface the parse error via runStartMsg,
// never a panic or a silently-empty panel.
func TestSlashRun_InvalidManifestShowsError(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.input.SetValue("/run " + filepath.Join(t.TempDir(), "does-not-exist.json"))
	model, cmd := m.Update(keyPress("enter"))
	mm := model.(Model)
	if cmd == nil {
		t.Fatal("expected a cmd from /run even for a bad path (cmdRunStart returns an error-carrying cmd)")
	}
	msg := cmd()
	rsm, ok := msg.(runStartMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runStartMsg", msg)
	}
	if rsm.err == nil {
		t.Fatal("expected a parse error for a missing manifest file")
	}
	model2, _ := mm.Update(rsm)
	mm2 := model2.(Model)
	if mm2.runPanelVisible {
		t.Fatal("panel must not open on a failed run.start")
	}
	if !mm2.msgPanelVisible {
		t.Fatal("expected the error message panel to open")
	}
}

// TestSlashRun_ValidManifestStartsRunAndOpensPanel is the Fase 4 happy path:
// /run parses the manifest locally, sends its content (not the path) to
// RunStart, and a successful response opens the panel and tracks the run.
func TestSlashRun_ValidManifestStartsRunAndOpensPanel(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{
		runStartRes: &daemon.RunResult{ID: "run-1", Status: daemon.RunRunning},
	}
	m.SetClient(fc)
	path := writeTestManifest(t, "run-1")
	m.input.SetValue("/run " + path)
	model, cmd := m.Update(keyPress("enter"))
	mm := model.(Model)
	if cmd == nil {
		t.Fatal("expected a cmd from /run")
	}
	msg := cmd()
	rsm, ok := msg.(runStartMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runStartMsg", msg)
	}
	if rsm.err != nil {
		t.Fatalf("cmdRunStart error = %v", rsm.err)
	}
	// The manifest that reached the fake client must be the parsed content,
	// not a path — RunStartParams' whole point (Fase 0/3 decision).
	if fc.runStartMani.RunID != "run-1" || fc.runStartMani.Goal != "implement feature" {
		t.Fatalf("client saw manifest %+v, want the parsed run-1 manifest", fc.runStartMani)
	}

	model2, pollCmd := mm.Update(rsm)
	mm2 := model2.(Model)
	if mm2.runID != "run-1" {
		t.Fatalf("runID = %q, want run-1", mm2.runID)
	}
	if !mm2.runPanelVisible {
		t.Fatal("expected the run panel to open on a successful run.start")
	}
	if pollCmd == nil {
		t.Fatal("expected a status-poll tick to be scheduled for a RunRunning result")
	}
	if !mm2.runPollPending {
		t.Fatal("runPollPending should be set while a poll tick is in flight")
	}
}

// TestRunPanel_ApproveAndDeclineKeys covers y/n handling for a pending
// checkpoint — only meaningful when PendingCheckpoint is actually set.
func TestRunPanel_ApproveAndDeclineKeys(t *testing.T) {
	fc := &fakeClient{runApproveRes: &daemon.RunResult{ID: "run-1", Status: daemon.RunRunning}}
	m := newTestModel()
	m.SetClient(fc)
	m.runID = "run-1"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{
		ID:                "run-1",
		Status:            daemon.RunPausedCheckpoint,
		PendingCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
	}

	model, cmd := m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("expected a cmd from approving a pending checkpoint")
	}
	msg := cmd()
	ram, ok := msg.(runApproveMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runApproveMsg", msg)
	}
	if ram.err != nil {
		t.Fatalf("approve error = %v", ram.err)
	}
	if !fc.runApproveCall.called || fc.runApproveCall.runID != "run-1" || !fc.runApproveCall.approved {
		t.Fatalf("client approve call = %+v, want runID=run-1 approved=true", fc.runApproveCall)
	}
	mm := model.(Model)
	if !mm.runPanelVisible {
		t.Fatal("panel should stay open across an approval")
	}

	// Fresh model for decline, so the two calls don't interfere.
	fc2 := &fakeClient{runApproveRes: &daemon.RunResult{ID: "run-1", Status: daemon.RunKilled}}
	m2 := newTestModel()
	m2.SetClient(fc2)
	m2.runID = "run-1"
	m2.runPanelVisible = true
	m2.runResult = &daemon.RunResult{
		ID:                "run-1",
		Status:            daemon.RunPausedCheckpoint,
		PendingCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
	}
	_, cmd2 := m2.Update(keyPress("n"))
	if cmd2 == nil {
		t.Fatal("expected a cmd from declining a pending checkpoint")
	}
	_ = cmd2()
	if !fc2.runApproveCall.called || fc2.runApproveCall.approved {
		t.Fatalf("client approve call = %+v, want approved=false", fc2.runApproveCall)
	}
}

// TestRunPanel_ApproveRacySnapshotStillPolls is a regression lock for a
// real bug found live driving the TUI through the QA harness
// (hojaDeRuta-qa-autonomo-tui.md Fase 4, scenarios B3/B6): approving or
// declining a checkpoint that was the run's LAST one before completion
// left the panel frozen forever showing the checkpoint as still pending.
// Root cause: ApproveRunCheckpoint's own returned snapshot is racy by
// design (internal/daemon/runs.go) — taken right after handing the
// decision to the run's goroutine, which may not have processed it yet —
// so it can still legitimately report Status == RunPausedCheckpoint even
// though the decision already went through server-side. The old code only
// rescheduled polling when Status == RunRunning, so this exact snapshot
// never triggered another poll and the panel never learned the real
// outcome. The fix polls again whenever Report == nil (the same
// genuinely-terminal signal used everywhere else in this codebase),
// regardless of which non-terminal Status a racy snapshot happens to show.
func TestRunPanel_ApproveRacySnapshotStillPolls(t *testing.T) {
	fc := &fakeClient{
		// The racy snapshot: still shows the checkpoint pending even though
		// the decision was already accepted server-side.
		runApproveRes: &daemon.RunResult{
			ID: "run-1", Status: daemon.RunPausedCheckpoint,
			PendingCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
			Report:            nil,
		},
		runStatusRes: &daemon.RunResult{ID: "run-1", Status: daemon.RunDone, Report: &run.Report{Status: run.StatusCompleted}},
	}
	m := newTestModel()
	m.SetClient(fc)
	m.runID = "run-1"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{
		ID: "run-1", Status: daemon.RunPausedCheckpoint,
		PendingCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
	}

	_, cmd := m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("expected a cmd from approving")
	}
	msg := cmd()
	ram, ok := msg.(runApproveMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runApproveMsg", msg)
	}

	model2, pollCmd := m.Update(ram)
	mm2 := model2.(Model)
	if pollCmd == nil {
		t.Fatal("a racy RunPausedCheckpoint snapshot with Report == nil must still schedule another poll — this is the exact bug found live (B3/B6), the panel would otherwise freeze forever")
	}
	if !mm2.runPollPending {
		t.Fatal("runPollPending should be set after rescheduling from the racy snapshot")
	}

	// Confirm the follow-up poll actually reaches the real, settled state
	// (not stuck echoing the same stale snapshot forever).
	tickMsg := pollCmd()
	statusCmd, ok := tickMsg.(runStatusTickMsg)
	_ = ok
	model3, statusPollCmd := mm2.Update(statusCmd)
	_ = model3
	if statusPollCmd == nil {
		t.Fatal("expected the tick to trigger a status fetch")
	}
	finalMsg := statusPollCmd()
	rsm, ok := finalMsg.(runStatusMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runStatusMsg", finalMsg)
	}
	if rsm.res == nil || rsm.res.Report == nil || rsm.res.Report.Status != run.StatusCompleted {
		t.Fatalf("follow-up poll should reveal the real completed state, got %+v", rsm.res)
	}
}

// TestRunPanel_YNIgnoredWithoutPendingCheckpoint ensures y/n are no-ops when
// there's nothing to approve — they must not fire a spurious RPC.
func TestRunPanel_YNIgnoredWithoutPendingCheckpoint(t *testing.T) {
	fc := &fakeClient{}
	m := newTestModel()
	m.SetClient(fc)
	m.runID = "run-1"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{ID: "run-1", Status: daemon.RunRunning}

	_, cmd := m.Update(keyPress("y"))
	if cmd != nil {
		t.Fatal("y with no pending checkpoint must not produce a cmd")
	}
	if fc.runApproveCall.called {
		t.Fatal("approve RPC must not fire without a pending checkpoint")
	}
}

// TestRunPanel_CancelKey covers c → RunCancel.
func TestRunPanel_CancelKey(t *testing.T) {
	fc := &fakeClient{runCancelRes: &daemon.RunResult{ID: "run-1", Status: daemon.RunCanceled}}
	m := newTestModel()
	m.SetClient(fc)
	m.runID = "run-1"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{ID: "run-1", Status: daemon.RunRunning}

	_, cmd := m.Update(keyPress("c"))
	if cmd == nil {
		t.Fatal("expected a cmd from cancel")
	}
	msg := cmd()
	rcm, ok := msg.(runCancelMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runCancelMsg", msg)
	}
	if rcm.err != nil {
		t.Fatalf("cancel error = %v", rcm.err)
	}
	if fc.runCancelCall != "run-1" {
		t.Fatalf("client cancel call = %q, want run-1", fc.runCancelCall)
	}
}

// TestRunPanel_EscClosesWithoutCanceling verifies esc only hides the panel —
// the run itself, and its tracking, are unaffected (ctrl+4 can reopen it).
func TestRunPanel_EscClosesWithoutCanceling(t *testing.T) {
	fc := &fakeClient{}
	m := newTestModel()
	m.SetClient(fc)
	m.runID = "run-1"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{ID: "run-1", Status: daemon.RunRunning}

	model, cmd := m.Update(keyPress("esc"))
	if cmd != nil {
		t.Fatal("esc must not itself trigger an RPC")
	}
	mm := model.(Model)
	if mm.runPanelVisible {
		t.Fatal("esc should close the run panel")
	}
	if mm.runID != "run-1" {
		t.Fatal("esc must not stop tracking the run")
	}
	if fc.runCancelCall != "" {
		t.Fatal("esc must not cancel the run")
	}
}

// TestCtrl4_TogglesRunPanel covers the reopen-via-shortcut path from the
// roadmap ("queda accesible desde un atajo"): no tracked run → toast; a
// tracked run → toggles visibility both ways.
func TestCtrl4_TogglesRunPanel(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	model, _ := m.Update(keyPress("ctrl+4"))
	mm := model.(Model)
	if mm.runPanelVisible {
		t.Fatal("ctrl+4 must not open the panel with no tracked run")
	}
	if !strings.Contains(mm.toast, "no active run") {
		t.Fatalf("toast = %q, want a no-active-run hint", mm.toast)
	}

	mm.runID = "run-1"
	model2, _ := mm.Update(keyPress("ctrl+4"))
	mm2 := model2.(Model)
	if !mm2.runPanelVisible {
		t.Fatal("ctrl+4 should open the panel once a run is tracked")
	}
	model3, _ := mm2.Update(keyPress("ctrl+4"))
	mm3 := model3.(Model)
	if mm3.runPanelVisible {
		t.Fatal("a second ctrl+4 should close the panel again")
	}
}

// TestRunCheckpointEvent_OpensPanelAndFetchesStatus is the "notificación
// entrante → el panel se abre solo" requirement: an incoming
// run.checkpoint.event must track the run and open the panel even if this
// TUI never called /run itself (e.g. another client started it), and
// trigger an immediate status fetch since the notification payload itself
// doesn't carry the full RunResult snapshot.
func TestRunCheckpointEvent_OpensPanelAndFetchesStatus(t *testing.T) {
	fc := &fakeClient{runStatusRes: &daemon.RunResult{ID: "run-2", Status: daemon.RunPausedCheckpoint}}
	m := newTestModel()
	m.SetClient(fc)

	payload := daemon.RunCheckpointEventPayload{
		RunID:      "run-2",
		SessionID:  "sess-2",
		Checkpoint: "pre-merge",
		Trigger:    run.TriggerBeforeMerge,
		Reason:     "checkpoint pending approval",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	notif := daemon.JSONRPCNotification{JSONRPC: "2.0", Method: daemon.MethodRunCheckpointEvent, Params: data}

	cmd := m.handleDaemonEvent(notif)
	if m.runID != "run-2" {
		t.Fatalf("runID = %q, want run-2 (picked up from the event, not started locally)", m.runID)
	}
	if !m.runPanelVisible {
		t.Fatal("expected the panel to auto-open on an incoming checkpoint event")
	}
	if cmd == nil {
		t.Fatal("expected a status-fetch cmd to follow the event")
	}
	msg := cmd()
	rsm, ok := msg.(runStatusMsg)
	if !ok {
		t.Fatalf("msg type = %T, want runStatusMsg", msg)
	}
	if rsm.res == nil || rsm.res.ID != "run-2" {
		t.Fatalf("status result = %+v, want run-2", rsm.res)
	}
	if len(fc.runStatusCalls) != 1 || fc.runStatusCalls[0] != "run-2" {
		t.Fatalf("run.status calls = %v, want exactly [run-2]", fc.runStatusCalls)
	}
}

// TestRunStatusPoll_ReschedulesWhileRunningStopsWhenTerminal covers the
// poll-tick lifecycle: runStatusMsg reschedules another tick only while the
// run is still RunRunning; a terminal (or paused-checkpoint) result lets
// the loop end instead of polling forever.
func TestRunStatusPoll_ReschedulesWhileRunningStopsWhenTerminal(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.runID = "run-3"

	// A stale tick for a run we're no longer tracking must be dropped
	// silently (no RPC, no reschedule) — see runStatusTickMsg's own doc.
	model, cmd := m.Update(runStatusTickMsg{runID: "stale-run"})
	mm := model.(Model)
	if cmd != nil {
		t.Fatal("a stale tick (wrong run_id) must not produce a follow-up cmd")
	}
	if mm.runPollPending {
		t.Fatal("a stale tick must still clear runPollPending")
	}

	// A live tick for the tracked run fetches status.
	model2, cmd2 := mm.Update(runStatusTickMsg{runID: "run-3"})
	mm2 := model2.(Model)
	if cmd2 == nil {
		t.Fatal("expected the tick to trigger a status fetch")
	}
	_ = cmd2()
	_ = mm2

	// RunRunning reschedules another tick.
	model3, cmd3 := mm2.Update(runStatusMsg{res: &daemon.RunResult{ID: "run-3", Status: daemon.RunRunning}})
	mm3 := model3.(Model)
	if cmd3 == nil {
		t.Fatal("expected a reschedule cmd while the run is still RunRunning")
	}
	if !mm3.runPollPending {
		t.Fatal("runPollPending should be set again after rescheduling")
	}

	// A terminal status does not reschedule. Report must be set here to
	// match reality: the daemon only ever reports Status == RunDone
	// together with a non-nil Report (finishRun, internal/daemon/runs.go)
	// — Report == nil, not Status, is the genuinely-terminal signal the
	// Model actually gates on (see runStatusMsg/runApproveMsg/runStartMsg
	// and TestRunPanel_ApproveRacySnapshotStillPolls for why: a racy
	// snapshot can report a non-terminal Status with Report == nil and
	// still needs another poll).
	mm3.runPollPending = false
	model4, cmd4 := mm3.Update(runStatusMsg{res: &daemon.RunResult{ID: "run-3", Status: daemon.RunDone, Report: &run.Report{Status: run.StatusCompleted}}})
	mm4 := model4.(Model)
	if cmd4 != nil {
		t.Fatal("a terminal run.status result must not reschedule another poll")
	}
	if mm4.runPollPending {
		t.Fatal("runPollPending must stay false once the run is terminal")
	}
}

// TestRunPanel_ClearsOnSessionSwitch is a regression lock for a real bug
// found live driving the TUI through the QA harness
// (hojaDeRuta-qa-autonomo-tui.md Fase 4, scenario B5): a run belongs to
// whichever session started it, not to the TUI process globally, but the
// run panel's tracking state (runID/runResult/runPanelVisible/
// runPollPending) previously survived a session switch untouched. A ctrl+4
// in a brand-new session that never ran anything showed a STALE run left
// over from the session you just switched away from, instead of "no active
// run". The fix (resetRunPanelForSessionSwitch) clears that state at every
// site that already resets entries/lastSeq/pendingUserText for the same
// reason; this test drives one of those sites (createSessionMsg, the "n"
// new-session path) end to end through ctrl+4.
func TestRunPanel_ClearsOnSessionSwitch(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	// Simulate a session that has a tracked, visible run — exactly the
	// state ctrl+4 would otherwise resurrect after switching sessions.
	m.runID = "run-old"
	m.runPanelVisible = true
	m.runPollPending = true
	m.runResult = &daemon.RunResult{ID: "run-old", Status: daemon.RunRunning}
	m.runErr = "stale error from the old session"

	model, _ := m.Update(createSessionMsg{res: &daemon.SessionResult{ID: "sess-new"}})
	mm := model.(Model)

	if mm.runID != "" {
		t.Fatalf("runID = %q, want cleared after switching sessions", mm.runID)
	}
	if mm.runResult != nil {
		t.Fatal("runResult should be cleared after switching sessions")
	}
	if mm.runErr != "" {
		t.Fatalf("runErr = %q, want cleared after switching sessions", mm.runErr)
	}
	if mm.runPanelVisible {
		t.Fatal("runPanelVisible should be cleared after switching sessions")
	}
	if mm.runPollPending {
		t.Fatal("runPollPending should be cleared after switching sessions")
	}

	// ctrl+4 in the new session must report "no active run", not resurrect
	// the old session's run — this is the exact user-visible symptom (B5).
	model2, _ := mm.Update(keyPress("ctrl+4"))
	mm2 := model2.(Model)
	if mm2.runPanelVisible {
		t.Fatal("ctrl+4 must not open the panel — the new session has no tracked run")
	}
	if !strings.Contains(mm2.toast, "no active run") {
		t.Fatalf("toast = %q, want a no-active-run hint", mm2.toast)
	}
}

// TestRunPanel_View covers renderRunPanel producing the expected content for
// a pending checkpoint, so the golden-ish substrings this panel promises
// (checkpoint id/trigger, y/n hint) don't silently regress.
func TestRunPanel_View(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.SetSize(100, 30)
	m.runID = "run-4"
	m.runPanelVisible = true
	m.runResult = &daemon.RunResult{
		ID:                "run-4",
		Status:            daemon.RunPausedCheckpoint,
		TokensUsed:        1200,
		IterUsed:          3,
		PendingCheckpoint: &run.Checkpoint{ID: "pre-merge", Trigger: run.TriggerBeforeMerge, Required: true},
	}
	view := m.View().Content
	if !strings.Contains(view, "run-4") {
		t.Fatal("view should show the tracked run id")
	}
	if !strings.Contains(view, "pre-merge") {
		t.Fatal("view should show the pending checkpoint id")
	}
	if !strings.Contains(view, "y approve") || !strings.Contains(view, "n decline") {
		t.Fatal("view should show the approve/decline hint while a checkpoint is pending")
	}
}
