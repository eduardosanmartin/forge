// Package e2e — Fase 7 verification (hojaDeRuta-multiagente.md): the
// roadmap's closing checklist. Four real-daemon scenarios Fases 1-6's own
// tests didn't individually cover end-to-end:
//   - --decompose (no explicit tasks) through a required checkpoint, via
//     the CLI AND via the TUI's own RPC adapter, reaching the same result.
//   - --resume against a run left genuinely pending (never approved,
//     never declined) across a real daemon restart — a new SessionManager
//     instance over the same on-disk state, not just a fresh RPC call.
//   - --verify-audit validating the hash-chained audit log a
//     daemon-hosted run (not the old in-process one) produced under
//     regulado sensitivity (RNF-4.10).
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
	"github.com/eduardosanmartin/forge/internal/tui"
)

// TestManifestRun_DecomposeThenCheckpointViaCLI drives a manifest with NO
// explicit tasks through --decompose (the daemon's own manifestDecomposer,
// internal/daemon/runs.go, asks the scripted model for a task list) and a
// required before_merge checkpoint, approved via --yes — the CLI half of
// Fase 7's "aprobado desde la TUI y desde la CLI, mismo resultado final".
func TestManifestRun_DecomposeThenCheckpointViaCLI(t *testing.T) {
	decomposed := `[{"id":"t1","goal":"decomposed task one"}]`
	srv := newScriptServer(t, "mock-7b", respFinal(decomposed), respFinal("task one done"))
	s := newManifestStack(t, srv.URL(), []string{"mock-7b"})
	pointHomeAtDaemon(t, s.transport.Addr())

	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSONNoTasks("e2e-run-decompose-cli", true)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--decompose", "--yes")
	if err != nil {
		t.Fatalf("run --manifest --decompose --yes failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "e2e-run-decompose-cli") || !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should show the decomposed run completed, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1/1 tasks") {
		t.Errorf("stdout should report the ONE decomposed task completed, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "[HITL auto-approved] pre-merge (before_merge)") {
		t.Errorf("stderr missing the auto-approved HITL line, got:\n%s", stderr)
	}
}

// TestManifestRun_TUIAdapterDecomposeThenCheckpoint is the TUI half: it
// drives the exact same scenario through tui.NewClientAdapter (the real
// wire adapter internal/tui/model.go calls from /run, y/n, and ctrl+4 —
// see run_panel_test.go, which already unit-tests Model's state machine
// against a fake client) against a REAL daemon, proving the adapter's own
// RunStart/RunStatus/RunApproveCheckpoint wire format round-trips
// correctly and reaches the SAME final report the CLI test above does.
//
// This does not drive Model's keyboard-input loop (that needs a real
// terminal or a bubbletea-key-event harness this package doesn't have —
// see hojaDeRuta-multiagente.md's Fase 4 notes on the same limitation);
// what it closes is the one seam Fase 4's fakeClient-based unit tests
// couldn't reach: does the adapter's real RPC round-trip actually work.
// Model's key/state handling given a correctly-shaped RunResult is already
// covered there.
func TestManifestRun_TUIAdapterDecomposeThenCheckpoint(t *testing.T) {
	decomposed := `[{"id":"t1","goal":"decomposed task one"}]`
	srv := newScriptServer(t, "mock-7b", respFinal(decomposed), respFinal("task one done"))
	s := newManifestStack(t, srv.URL(), []string{"mock-7b"})

	runID := "e2e-run-decompose-tui"
	mani, err := run.Parse([]byte(e2eManifestJSONNoTasks(runID, true)))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	adapter := tui.NewClientAdapter(s.client)
	started, err := adapter.RunStart(*mani, t.TempDir(), true)
	if err != nil {
		t.Fatalf("adapter.RunStart: %v", err)
	}
	if started.ID != runID {
		t.Fatalf("started.ID = %q, want %q", started.ID, runID)
	}

	deadline := time.Now().Add(15 * time.Second)
	var res *daemon.RunResult
	for time.Now().Before(deadline) {
		res, err = adapter.RunStatus(runID)
		if err != nil {
			t.Fatalf("adapter.RunStatus: %v", err)
		}
		if res.PendingCheckpoint != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res == nil || res.PendingCheckpoint == nil {
		t.Fatalf("checkpoint never became pending, last status = %+v", res)
	}
	if res.PendingCheckpoint.ID != "pre-merge" {
		t.Fatalf("pending checkpoint = %+v, want pre-merge", res.PendingCheckpoint)
	}

	// ApproveRunCheckpoint's own returned snapshot is racy by design (see
	// internal/daemon/runs_test.go's isRunTerminal doc comment): it's taken
	// right after handing the decision to the run's goroutine, which may
	// not have processed it yet, so it can still legitimately read
	// RunPausedCheckpoint here — only the FOLLOW-UP poll below is the
	// reliable completion signal. This call only needs to not error.
	if _, err := adapter.RunApproveCheckpoint(runID, true); err != nil {
		t.Fatalf("adapter.RunApproveCheckpoint: %v", err)
	}

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err = adapter.RunStatus(runID)
		if err != nil {
			t.Fatalf("adapter.RunStatus: %v", err)
		}
		if res.Report != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Report == nil {
		t.Fatalf("run never reached a terminal report, last status = %+v", res)
	}
	// Same final shape TestManifestRun_DecomposeThenCheckpointViaCLI
	// verifies via its printed report — here checked directly against the
	// structured RunResult the TUI panel itself renders from.
	if res.Status != daemon.RunDone {
		t.Fatalf("final status = %q, want %q", res.Status, daemon.RunDone)
	}
	if res.Report.Status != run.StatusCompleted || len(res.Report.CompletedTasks) != 1 || res.Report.TotalTasks != 1 {
		t.Fatalf("report = %+v, want 1/1 completed tasks", res.Report)
	}
}

// e2eManifestJSONNoTasks is e2eManifestJSON's --decompose counterpart: same
// shape, but omits "tasks" entirely (required for --decompose — Runner.Run
// refuses --decompose against a manifest that already declares tasks).
func e2eManifestJSONNoTasks(runID string, withCheckpoint bool) string {
	hitl := ""
	if withCheckpoint {
		hitl = `,"hitl":{"checkpoints":[{"id":"pre-merge","trigger":"before_merge","required":true}]}`
	}
	return `{
  "run_id": "` + runID + `",
  "mode": "checkpoint",
  "goal": "e2e test goal",
  "spec": "SPEC stub",
  "budget": {"max_wall_clock": "5m", "max_tokens": 50000, "max_iterations": 10, "max_retries_per_task": 1},
  "git": {"isolation": "branch", "base_branch": "main", "work_branch": "run/` + runID + `", "commit_per_task": false, "merge_to_base": "manual"}` +
		hitl + `
}`
}

// TestManifestRun_ResumeAfterDaemonRestart is Fase 7's second checklist
// item: a run left genuinely pending at a checkpoint (never approved,
// never declined — no client ever called run.approve_checkpoint) across a
// SECOND, independent SessionManager/Transport instance built over the
// exact same on-disk store and --state-dir, reproducing what a real crash
// and `forge serve` restart looks like far more faithfully than reusing
// the same in-process daemon would.
func TestManifestRun_ResumeAfterDaemonRestart(t *testing.T) {
	const runID = "e2e-run-restart"
	srv := newScriptServer(t, "mock-7b", respFinal("task one done"))

	ws := ownTempDir(t, "forge-e2e-restart-ws")
	initGitRepo(t, ws)
	storagePath := filepath.Join(ownTempDir(t, "forge-e2e-restart-home"), "forge.db")
	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSON(runID, true)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// "Daemon A": start the run directly over RPC (not through the CLI —
	// the CLI always submits an immediate HITL decision the instant it
	// sees the checkpoint, and this test specifically needs one that
	// nobody ever resolves, matching a real crash).
	sA := newManifestStackOpts(t, srv.URL(), []string{"mock-7b"}, manifestStackOpts{workspace: ws, storagePath: storagePath})
	mani, err := run.Parse([]byte(e2eManifestJSON(runID, true)))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	var startRes daemon.RunResult
	if err := sA.call(daemon.MethodRunStart, daemon.RunStartParams{Manifest: *mani, StateDir: stateDir}, &startRes); err != nil {
		t.Fatalf("run.start: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var pending bool
	for time.Now().Before(deadline) {
		var res daemon.RunResult
		if err := sA.call(daemon.MethodRunStatus, daemon.RunStatusParams{RunID: runID}, &res); err == nil && res.PendingCheckpoint != nil {
			pending = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !pending {
		t.Fatal("checkpoint never became pending on daemon A")
	}

	// "Crash": close A's client so nothing in this test can reach it
	// again — Transport.Stop isn't safe to call twice (it panics on a
	// double channel-close), and t.Cleanup already owns calling it once at
	// the end of the test, so closing the client is what actually matters
	// here: the goroutine blocked in OnCheckpoint on A's SessionManager
	// becomes exactly as unreachable as it would be after a real crash,
	// since nothing from here on ever calls back into it.
	_ = sA.client.Close()

	// "Daemon B": a brand new SessionManager/Transport, same store, same
	// --state-dir — the only thing connecting it to A is the state.json A
	// persisted BEFORE blocking (Fase 2's handleCheckpoint rewrite exists
	// specifically for this).
	sB := newManifestStackOpts(t, srv.URL(), []string{"mock-7b"}, manifestStackOpts{workspace: ws, storagePath: storagePath})
	pointHomeAtDaemon(t, sB.transport.Addr())

	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--resume", "--yes")
	if err != nil {
		t.Fatalf("--resume after restart failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, runID) || !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should show the resumed run completed, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1/1 tasks") {
		t.Errorf("stdout should report the task already-completed-on-A as still completed, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "[HITL auto-approved] pre-merge (before_merge)") {
		t.Errorf("stderr should show daemon B approving the checkpoint A left pending, got:\n%s", stderr)
	}
}

// TestManifestRun_VerifyAuditAfterDaemonRun is Fase 7's third checklist
// item: RNF-4.10's tamper-evident audit log, written by the DAEMON-hosted
// Runner (not the old in-process one) under regulado sensitivity, must
// still validate correctly through --verify-audit (unchanged, always
// local — see runVerifyAudit in internal/cli/run.go).
func TestManifestRun_VerifyAuditAfterDaemonRun(t *testing.T) {
	const runID = "e2e-run-audit"
	srv := newScriptServer(t, "mock-7b", respFinal("task one done"))
	s := newManifestStackOpts(t, srv.URL(), []string{"mock-7b"}, manifestStackOpts{sensitivity: "regulado"})
	pointHomeAtDaemon(t, s.transport.Addr())

	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	// regulado requires a required before_merge checkpoint (RNF-9.2) —
	// e2eManifestJSON(_, true) already declares exactly that.
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSON(runID, true)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--yes")
	if err != nil {
		t.Fatalf("run --manifest --yes (regulado) failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, runID) || !strings.Contains(stdout, "completed") {
		t.Fatalf("run should complete under regulado sensitivity, got:\n%s\nstderr=%s", stdout, stderr)
	}

	auditPath := filepath.Join(stateDir, ".forge", "runs", runID, "audit.jsonl")
	if _, statErr := os.Stat(auditPath); statErr != nil {
		t.Fatalf("expected a tamper-evident audit log at %s (RNF-4.10, regulado sensitivity): %v", auditPath, statErr)
	}

	vStdout, vStderr, vErr := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--verify-audit")
	if vErr != nil {
		t.Fatalf("--verify-audit failed against the daemon-produced log: %v\nstdout=%s\nstderr=%s", vErr, vStdout, vStderr)
	}
	if !strings.Contains(vStdout, "OK:") {
		t.Errorf("--verify-audit should report the chain intact, got:\n%s", vStdout)
	}

	// Sanity check the log actually has real, chained content — not an
	// empty file that would trivially "verify".
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Fatal("audit log is empty — nothing was actually verified")
	}
}
