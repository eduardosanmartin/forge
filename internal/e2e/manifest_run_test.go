// Package e2e — Fase 5 verification (hojaDeRuta-multiagente.md): proves
// `forge run --manifest` actually drives a manifest run end to end as a
// thin RPC client against a REAL daemon (transport -> handler ->
// SessionManager -> the daemon-hosted Runner from Fase 1/2/3), the way a
// real user invokes it — not just that internal/cli/run.go compiles.
//
// internal/cli's own test suite already locks the exact WORDING of every
// printed line (TestPrintManifestProgress/TestPrintManifestGuidance) and
// every flag-validation/local-only path (dry_run, --verify-audit, --resume
// pre-check) via unit tests with no daemon involved — this file covers the
// one thing those can't: that the new run.start/run.status/
// run.approve_checkpoint event-driven wiring actually completes a real run
// and reproduces the same terminal lines when driven through the real
// cobra command, not called directly.
package e2e

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/cli"
	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// newManifestStack duplicates newStack's body (harness_test.go) with the
// ONE thing that harness deliberately leaves unwired: SetDeltaPublisher —
// see internal/client/run_manifest_tooltick_test.go's own precedent for
// this exact same duplication-over-modifying-shared-infra choice. Without
// it, run.progress.event/run.checkpoint.event (session_mgr.go's
// onToolEvent's sibling publishers) are constructed but silently dropped —
// m.deltaPublisher stays nil, so a checkpointed run would block forever
// with no client ever finding out, and a completed run would produce no
// progress lines at all. Every manifest-run e2e test needs exactly this,
// so it gets its own constructor instead of a one-off wiring call repeated
// at each call site.
func newManifestStack(t *testing.T, baseURL string, models []string) *stack {
	t.Helper()
	return newManifestStackOpts(t, baseURL, models, manifestStackOpts{})
}

// manifestStackOpts lets a test pin down the workspace/storage path (and
// sensitivity ceiling) instead of getting fresh ones every call — needed
// for TestManifestRun_ResumeAfterDaemonRestart, which must reopen the SAME
// store and read the SAME .forge/runs/<run_id>/state.json a second daemon
// instance never wrote itself, exactly like a real `forge serve` restart.
// Zero value (manifestStackOpts{}) reproduces newManifestStack's own
// always-fresh behavior.
type manifestStackOpts struct {
	workspace   string // "" = create+chdir a fresh one
	storagePath string // "" = create a fresh one
	sensitivity string // "" = config.Defaults()'s own default
}

// newManifestStackOpts duplicates newStack's body (harness_test.go) with
// two things that harness deliberately leaves out for this migration's own
// tests: SetDeltaPublisher (see newManifestStack's doc comment above) and,
// here, the ability to pin workspace/storage path/sensitivity so a test can
// build a SECOND daemon instance over the SAME on-disk state as a first one
// it already tore down — simulating a real daemon restart.
func newManifestStackOpts(t *testing.T, baseURL string, models []string, opts manifestStackOpts) *stack {
	t.Helper()

	ws := opts.workspace
	if ws == "" {
		ws = ownTempDir(t, "forge-e2e-manifest-ws")
		initGitRepo(t, ws)
	}

	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir into workspace %s: %v", ws, err)
	}
	t.Cleanup(func() { _ = os.Chdir(initialWd()) })

	storagePath := opts.storagePath
	if storagePath == "" {
		storagePath = filepath.Join(ownTempDir(t, "forge-e2e-manifest-home"), "forge.db")
	}

	cfg := config.Defaults()
	cfg.Storage.Path = storagePath
	cfg.DefaultProvider = "ollama"
	cfg.Providers = map[string]config.Provider{
		"ollama": {Kind: "openai-compatible", BaseURL: baseURL, Models: models},
	}
	cfg.Network.AllowedHosts = []string{"127.0.0.1", "localhost"}
	if opts.sensitivity != "" {
		cfg.Project.Sensitivity = opts.sensitivity
	}
	pol := testPolicy()
	cfg.Permissions = config.PermissionsPolicy{
		FS:    config.FSPermissions{Read: pol.FS.Read, Write: pol.FS.Write},
		Shell: config.ShellPermissions{Allow: pol.Shell.Allow, RequireIsolation: false},
		Git:   config.GitPermissions{Allow: pol.Git.Allow},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	st, err := store.Open(cfg.Storage.Path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	llmReg, err := llm.New(cfg, cfg.Network.AllowedHosts, discardLogger())
	if err != nil {
		t.Fatalf("create llm registry: %v", err)
	}
	t.Cleanup(func() { _ = llmReg.Close() })

	permsEng, err := perms.New(pol, ws, nil)
	if err != nil {
		t.Fatalf("create perms engine: %v", err)
	}

	toolsReg := tools.NewDefaultRegistry(permsEng, ws, discardLogger())

	emergency := daemon.NewEmergencyState(discardLogger())
	mgr := daemon.NewSessionManager(st, llmReg, toolsReg, emergency, discardLogger(), cfg, permsEng, st)
	handler := daemon.NewHandler(mgr, discardLogger(), nil, nil)
	tx := daemon.NewTransport("127.0.0.1:0", handler, discardLogger())
	mgr.SetDeltaPublisher(func(sessionID string, notif *daemon.JSONRPCNotification) {
		tx.Broadcast(sessionID, notif)
	})
	if err := tx.Start(context.Background()); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { _ = tx.Stop() })

	cl, err := client.Connect(context.Background(), tx.Addr())
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	return &stack{t: t, workspace: ws, cfg: cfg, transport: tx, client: cl}
}

// pointHomeAtDaemon redirects HOME/USERPROFILE to a fresh temp dir and
// writes ~/.forge/daemon.addr so client.Connect(ctx, "") (what
// internal/cli/run.go's runManifest uses) resolves to this real test
// daemon, exactly as `forge serve` would leave behind for a real `forge
// run` invocation.
func pointHomeAtDaemon(t *testing.T, addr string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.addr"), []byte(addr), 0o644); err != nil {
		t.Fatalf("write daemon.addr: %v", err)
	}
}

// resetRunCommandFlags resets every leakable bool/string flag on the `run`
// subcommand back to its default — RootCommand is a shared singleton
// across every test in this package (runCLI reuses it, matching
// internal/cli/run_log_test.go's resetRunFlagsForTest), and pflag never
// resets a bound variable back to its default just because a later
// Parse() omits it. Once this package grew enough manifest-run tests that
// remembering "--yes=false"/"--decompose=false"/"--resume=false" at every
// call site became easy to get wrong (Fase 7 did, immediately), doing the
// reset centrally in runCLI itself is safer than relying on every call
// site to opt in.
func resetRunCommandFlags(t *testing.T) {
	t.Helper()
	cmd, _, err := cli.RootCommand.Find([]string{"run"})
	if err != nil {
		t.Fatalf("find run command: %v", err)
	}
	for _, name := range []string{"manifest", "state-dir", "log", "resume", "yes", "decompose", "verify-audit", "json", "session"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
}

// runCLI executes the real forge CLI (same RootCommand singleton
// production uses) with args, capturing real os.Stdout/os.Stderr — needed
// because runManifest's progress/report writers are hardcoded to
// os.Stdout/os.Stderr (matching production, not cobra's OutOrStdout/
// ErrOrStderr redirection) exactly as they were before Fase 5.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetRunCommandFlags(t)

	origStdout, origStderr := os.Stdout, os.Stderr
	rOut, wOut, pErr := os.Pipe()
	if pErr != nil {
		t.Fatalf("pipe stdout: %v", pErr)
	}
	rErr, wErr, pErr2 := os.Pipe()
	if pErr2 != nil {
		t.Fatalf("pipe stderr: %v", pErr2)
	}
	os.Stdout, os.Stderr = wOut, wErr

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, rOut); outCh <- b.String() }()
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, rErr); errCh <- b.String() }()

	root := cli.RootCommand
	root.SetArgs(args)
	runErr := root.ExecuteContext(context.Background())
	root.SetArgs(nil)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = origStdout, origStderr

	return <-outCh, <-errCh, runErr
}

// e2eManifestJSON builds a minimal, real manifest (branch isolation per
// RNF-8.1, matching the shape internal/daemon/runs_test.go's
// testRunManifest already exercises) — one task, and optionally one
// required before_merge checkpoint.
func e2eManifestJSON(runID string, withCheckpoint bool) string {
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
		hitl + `,
  "tasks": [{"id": "t1", "goal": "task one"}]
}`
}

// TestManifestRun_HappyPathViaRPC drives a no-checkpoint, single-task
// manifest through the real `forge run --manifest` command against a real
// daemon: proves run.start + run.status polling + the run.progress.event
// stream together produce a completed run with the correct progress lines,
// with no HITL involved.
func TestManifestRun_HappyPathViaRPC(t *testing.T) {
	srv := newScriptServer(t, "mock-7b", respFinal("task one done"))
	s := newManifestStack(t, srv.URL(), []string{"mock-7b"})
	pointHomeAtDaemon(t, s.transport.Addr())

	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSON("e2e-run-happy", false)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// --yes=false pinned explicitly for the same flag-leakage reason
	// TestManifestRun_CheckpointDeclinedWithoutYesViaRPC documents — this
	// manifest has no checkpoint so it's inert today, but pins the
	// invariant against future reordering/additions in this file.
	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--yes=false")
	if err != nil {
		t.Fatalf("run --manifest failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "e2e-run-happy") || !strings.Contains(stdout, "completed") {
		t.Errorf("stdout missing completed report, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1/1 tasks") {
		t.Errorf("stdout should report 1/1 tasks completed, got:\n%s", stdout)
	}
	// printManifestProgress's exact task_start/task_done wording (Fase 5's
	// whole reason for adding run.progress.event instead of relying on
	// run.status polling alone).
	if !strings.Contains(stderr, "[1/1] t1: iniciando") {
		t.Errorf("stderr missing task_start progress line, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "[1/1] t1: listo") {
		t.Errorf("stderr missing task_done progress line, got:\n%s", stderr)
	}

	if _, statErr := os.Stat(filepath.Join(stateDir, ".forge", "runs", "e2e-run-happy", "state.json")); statErr != nil {
		t.Errorf("expected state.json to be persisted: %v", statErr)
	}
}

// TestManifestRun_CheckpointAutoApprovedViaRPC drives a manifest with a
// required before_merge checkpoint through --yes: proves
// run.checkpoint.event reaches the CLI, handleCheckpointEvent submits
// run.approve_checkpoint automatically, and the SAME goroutine/session
// continues to completion — the exact live-checkpoint-approval path
// Fase 2 built and Fase 5 now exposes through the real command.
func TestManifestRun_CheckpointAutoApprovedViaRPC(t *testing.T) {
	srv := newScriptServer(t, "mock-7b", respFinal("task one done"))
	s := newManifestStack(t, srv.URL(), []string{"mock-7b"})
	pointHomeAtDaemon(t, s.transport.Addr())

	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSON("e2e-run-checkpoint", true)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--yes")
	if err != nil {
		t.Fatalf("run --manifest --yes failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	if !strings.Contains(stderr, "[HITL auto-approved] pre-merge (before_merge)") {
		t.Errorf("stderr missing the exact auto-approved HITL line, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "e2e-run-checkpoint") || !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should show the run completed past its checkpoint, got:\n%s", stdout)
	}
}

// TestManifestRun_CheckpointDeclinedWithoutYesViaRPC is the non-interactive
// decline path: without --yes, the CLI has never read stdin for HITL
// approval (see handleCheckpointEvent's doc comment) — it declines
// immediately, the daemon-hosted run pauses cleanly, and the command exits
// non-zero with guidance instead of hanging forever waiting on a decision
// nobody will send.
func TestManifestRun_CheckpointDeclinedWithoutYesViaRPC(t *testing.T) {
	srv := newScriptServer(t, "mock-7b", respFinal("task one done"))
	s := newManifestStack(t, srv.URL(), []string{"mock-7b"})
	pointHomeAtDaemon(t, s.transport.Addr())

	stateDir := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(e2eManifestJSON("e2e-run-decline", true)), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// --yes=false is explicit, not just the default: RootCommand is a shared
	// singleton across this package's tests (runCLI reuses it, same hazard
	// internal/cli/run_resume_test.go documents for --resume) — pflag never
	// resets a bound bool back to false just because a later Parse() omits
	// the flag, so TestManifestRun_CheckpointAutoApprovedViaRPC's --yes
	// would otherwise leak into this test as a stale true.
	stdout, stderr, err := runCLI(t, "run", "--manifest", manifestPath, "--state-dir", stateDir, "--yes=false")
	if err == nil {
		t.Fatalf("expected a non-nil error for a paused (declined) checkpoint\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "[HITL] checkpoint \"pre-merge\" (before_merge) requires approval") {
		t.Errorf("stderr missing the exact non-auto-approved HITL line, got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Pausado en checkpoint pre-merge") {
		t.Errorf("stdout missing the paused-awaiting-approval guidance, got:\n%s", stdout)
	}
}
