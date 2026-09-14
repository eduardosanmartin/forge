package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"

	"github.com/spf13/cobra"
)

// UsageError marks command-invocation mistakes (bad flags or arguments).
// The binary entrypoint translates it into exit code 2, keeping runtime
// failures at 1.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string {
	if e.Err == nil {
		return "usage error"
	}
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error { return e.Err }

func usageErrorf(format string, args ...any) *UsageError {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

func init() {
	RootCommand.AddCommand(newRunCommand())
}

func newRunCommand() *cobra.Command {
	var (
		jsonOut          bool
		sessionID        string
		enableRetrieval  bool
		enableCompaction bool
		enableAnchoring  bool
		enableRouting    bool
		enableSkills     bool
		manifestPath     string
		autoYes          bool
		stateDir         string
		resume           bool
		verifyAudit      bool
	)
	cmd := &cobra.Command{
		Use:   "run [--json] [--session <id>] [--retrieval] [--compaction] [--anchoring] [--routing] [--skills] <prompt>",
		Short: "Execute one prompt non-interactively through the daemon",
		Long: "Runs a single agent turn without entering the interactive REPL. " +
			"With --json only the structured OneShotResult is printed on stdout.\n\n" +
			"V1 features (opt-in):\n" +
			"  --retrieval    Enable selective context retrieval (RF-3.2)\n" +
			"  --compaction   Enable hierarchical conversation compaction (RF-3.3)\n" +
			"  --anchoring    Enable persistent anchored facts (RF-3.4/3.5)\n" +
			"  --routing      Enable cost-based model routing per step (RF-2.4/2.5)\n" +
			"                 (this build: selects the generation-step model from\n" +
			"                 providers.<name>.model_roles; other steps are deterministic\n" +
			"                 and need no model)\n" +
			"  --skills       Enable skills lazy-load injection (RF-4.2)\n\n" +
			"Manifest mode (RF-11 autonomous):\n" +
			"  --manifest <path>  Execute a run manifest (JSON) through the same agent loop with\n" +
			"                     subagents, HITL checkpoints, and hard budget walls (RNF-8).\n" +
			"                     Sensitivity ceiling from .forge/config.json caps autonomy (RNF-9).\n" +
			"  --yes              Auto-approve HITL checkpoints (for CI/tests; otherwise pauses).\n" +
			"  --state-dir <dir>  Directory for run state/report persistence (default: current dir).\n" +
			"  --resume           Continue a run interrupted by a crash, client disconnect, or HITL\n" +
			"                     pause (RF-11.8) instead of starting over: reloads state.json for this\n" +
			"                     manifest's run_id from --state-dir, skips tasks already completed, and\n" +
			"                     reuses the original run's session so context isn't lost. Requires\n" +
			"                     --manifest to point at the SAME manifest file (same run_id) as the\n" +
			"                     interrupted run; refuses to resume a completed/failed/killed run.\n" +
			"  --verify-audit     Verify the tamper-evident audit log (RNF-4.10, written only under\n" +
			"                     regulado/datos-sensibles sensitivity) instead of running anything —\n" +
			"                     recomputes the hash chain and reports whether it's intact.",
		Args: func(cmd *cobra.Command, args []string) error {
			if manifestPath != "" {
				if len(args) != 0 {
					return usageErrorf("run with --manifest takes no prompt argument, got %d", len(args))
				}
				if strings.TrimSpace(manifestPath) == "" {
					return usageErrorf("--manifest path must not be empty")
				}
				if resume && verifyAudit {
					return usageErrorf("--resume and --verify-audit are mutually exclusive")
				}
				return nil
			}
			if resume {
				return usageErrorf("--resume requires --manifest")
			}
			if verifyAudit {
				return usageErrorf("--verify-audit requires --manifest")
			}
			if len(args) != 1 {
				return usageErrorf("run accepts exactly 1 prompt argument, got %d", len(args))
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageErrorf("prompt must not be empty")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if manifestPath != "" {
				if verifyAudit {
					return runVerifyAudit(cmd.OutOrStdout(), manifestPath, stateDir, jsonOut)
				}
				return runManifest(cmd.Context(), manifestPath, jsonOut, autoYes, stateDir, resume)
			}
			return runRun(cmd.Context(), args[0], sessionID, jsonOut,
				enableRetrieval, enableCompaction, enableAnchoring, enableRouting, enableSkills)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print only the machine-readable JSON result on stdout")
	cmd.Flags().StringVar(&sessionID, "session", "", "reuse an existing session id (default: ephemeral session)")
	cmd.Flags().BoolVar(&enableRetrieval, "retrieval", false, "enable selective context retrieval (v1)")
	cmd.Flags().BoolVar(&enableCompaction, "compaction", false, "enable hierarchical compaction (v1)")
	cmd.Flags().BoolVar(&enableAnchoring, "anchoring", false, "enable persistent anchored facts (v1)")
	cmd.Flags().BoolVar(&enableRouting, "routing", false, "enable cost-based model routing (v1)")
	cmd.Flags().BoolVar(&enableSkills, "skills", false, "enable skills lazy-load injection (v1)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "execute a run manifest file (RF-11) instead of a single prompt")
	cmd.Flags().BoolVar(&autoYes, "yes", false, "auto-approve HITL checkpoints in manifest mode")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "directory for run state/report persistence (default: current directory)")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume a manifest run interrupted by a crash, disconnect, or HITL pause (RF-11.8) instead of starting over")
	cmd.Flags().BoolVar(&verifyAudit, "verify-audit", false, "verify the tamper-evident audit log (RNF-4.10) instead of running anything")
	return cmd
}

// runVerifyAudit checks the hash chain of the audit log for the manifest at
// manifestPath's run_id, without running anything (RNF-4.10). It exists
// independently of a live daemon or Runner — verification only needs to
// read a file.
func runVerifyAudit(out io.Writer, manifestPath, stateDir string, jsonOut bool) error {
	mani, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	if stateDir == "" {
		stateDir = "."
	}
	path := filepath.Join(stateDir, ".forge", "runs", mani.RunID, "audit.jsonl")

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		result := run.VerifyResult{Path: path, Valid: false, Reason: "no audit log found — this run's sensitivity may not have required one (RNF-4.10 applies only under regulado/datos-sensibles), or --state-dir doesn't match the original run"}
		if jsonOut {
			return writeJSONResultEnvelope(out, "run", result)
		}
		fmt.Fprintf(out, "no audit log at %s\n%s\n", path, result.Reason)
		return fmt.Errorf("audit log not found at %s", path)
	}

	res, err := run.VerifyAuditLog(path)
	if err != nil {
		return fmt.Errorf("verify audit log: %w", err)
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "run", res)
	}
	if res.Valid {
		fmt.Fprintf(out, "OK: %s — %d records, chain intact\n", res.Path, res.Records)
		return nil
	}
	fmt.Fprintf(out, "TAMPERED: %s — %d records read, chain broken at record %d\n  %s\n", res.Path, res.Records, res.BrokenAt, res.Reason)
	return fmt.Errorf("audit log %s failed verification at record %d", path, res.BrokenAt)
}

func runRun(ctx context.Context, prompt, sessionID string, jsonOut bool,
	enableRetrieval, enableCompaction, enableAnchoring, enableRouting, enableSkills bool) error {

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	res, err := client.RunOneShot(ctx, cl, prompt, client.RunOptions{
		SessionID:        sessionID,
		EnableRetrieval:  enableRetrieval,
		EnableCompaction: enableCompaction,
		EnableAnchoring:  enableAnchoring,
		EnableRouting:    enableRouting,
		EnableSkills:     enableSkills,
	})
	if err != nil {
		return fmt.Errorf("one-shot turn failed: %w", err)
	}

	if jsonOut {
		if err := writeJSONResult(os.Stdout, res); err != nil {
			return fmt.Errorf("write JSON result: %w", err)
		}
		return nil
	}

	writeHumanResult(os.Stdout, os.Stderr, res)
	return nil
}

func writeJSONResult(out io.Writer, res *client.OneShotResult) error {
	return writeJSONResultEnvelope(out, "run", res)
}

func writeHumanResult(stdout, stderr io.Writer, res *client.OneShotResult) {
	for _, tc := range res.ToolCalls {
		fmt.Fprintf(stderr, "-> %s(%s)\n", tc.Name, previewToolArgs(tc.Args))
		if tc.OK {
			fmt.Fprintln(stderr, "<- ok")
		} else {
			fmt.Fprintln(stderr, "<- error")
		}
	}
	fmt.Fprintln(stdout, strings.TrimSpace(res.Response))
}

func previewToolArgs(args json.RawMessage) string {
	return client.FormatToolArgs(args)
}

func runManifest(ctx context.Context, manifestPath string, jsonOut, autoYes bool, stateDir string, resume bool) error {
	app, _ := AppFromContext(ctx)
	if app == nil || app.Config == nil {
		return fmt.Errorf("configuration not loaded (internal error)")
	}
	cfg := app.Config

	// Parse manifest (JSON, spec_ref resolved relative to file).
	mani, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := mani.ValidateAgainstSensitivity(cfg); err != nil {
		return fmt.Errorf("sensitivity ceiling rejected manifest: %w", err)
	}
	if stateDir == "" {
		stateDir = "."
	}
	if resume && mani.Mode == "dry_run" {
		return &UsageError{Err: fmt.Errorf("--resume is not valid with mode dry_run: dry runs execute nothing and persist no state to resume from")}
	}

	var sessID string
	if resume {
		// RF-11.8: reuse the interrupted run's own session so the resumed
		// tasks see the same conversational context the earlier ones built
		// up, instead of starting the model cold. Resolved before connecting
		// to the daemon so an unresumable run (or missing state) fails fast
		// without requiring one to be running. Runner.Resume separately
		// validates the run itself is actually resumable (not completed/
		// failed/killed) and loads which tasks are already done.
		prev, lErr := run.LoadState(stateDir, mani.RunID)
		if lErr != nil {
			return fmt.Errorf("--resume: load previous state for run %q: %w", mani.RunID, lErr)
		}
		if prev.SessionID == "" {
			return fmt.Errorf("--resume: run %q has no session recorded in its persisted state, cannot continue its conversation", mani.RunID)
		}
		sessID = prev.SessionID
	}

	// Dry-run needs no daemon and no LLM.
	if mani.Mode == "dry_run" {
		r := newManifestRunner(mani, cfg, nil, autoYes, stateDir, "")
		rep, _ := r.Run(ctx)
		return writeManifestReport(rep, jsonOut)
	}

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	if !resume {
		// Create isolated session for the run (RNF-8.1 branch isolation primitive).
		sessID, err = createRunSession(ctx, cl, mani)
		if err != nil {
			return fmt.Errorf("create run session: %w", err)
		}
	}

	r := newManifestRunner(mani, cfg, client.ManifestExecutor(ctx, cl, sessID), autoYes, stateDir, sessID)
	var rep *run.Report
	var runErr error
	if resume {
		rep, runErr = r.Resume(ctx)
	} else {
		rep, runErr = r.Run(ctx)
	}
	// Always report, even when paused/killed.
	if rep != nil {
		if wErr := writeManifestReport(rep, jsonOut); wErr != nil {
			return wErr
		}
	}
	return runErr
}

func loadManifest(path string) (*run.Manifest, error) {
	// Delegates to run.ParseFile so spec_ref resolution and validation stay in one place.
	return run.ParseFile(path)
}

func createRunSession(ctx context.Context, cl *client.Client, mani *run.Manifest) (string, error) {
	meta := map[string]any{
		"source":      "run_manifest",
		"run_id":      mani.RunID,
		"mode":        mani.Mode,
		"work_branch": mani.Git.WorkBranch,
	}
	var res daemon.SessionResult
	if err := cl.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: meta}, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

func newManifestRunner(mani *run.Manifest, cfg *config.Config, exec run.Executor, autoYes bool, stateDir, sessionID string) *run.Runner {
	r := &run.Runner{
		Manifest:  mani,
		Config:    cfg,
		Executor:  exec,
		StateDir:  stateDir,
		SessionID: sessionID,
	}
	if autoYes {
		r.OnCheckpoint = func(cp run.Checkpoint, _ *run.RunState) (bool, error) {
			fmt.Fprintf(os.Stderr, "[HITL auto-approved] %s (%s)\n", cp.ID, cp.Trigger)
			return true, nil
		}
	} else {
		r.OnCheckpoint = func(cp run.Checkpoint, _ *run.RunState) (bool, error) {
			fmt.Fprintf(os.Stderr, "[HITL] checkpoint %q (%s) requires approval — run paused (re-run with --yes to auto-approve)\n", cp.ID, cp.Trigger)
			return false, nil
		}
	}
	return r
}

func writeManifestReport(rep *run.Report, jsonOut bool) error {
	if rep == nil {
		return nil
	}
	if jsonOut {
		return writeJSONResultEnvelope(os.Stdout, "run", rep)
	}
	fmt.Fprintf(os.Stdout, "run %s [%s] %s — %d/%d tasks, budget tokens %d iters %d\n",
		rep.RunID, rep.Mode, rep.Status, len(rep.CompletedTasks), rep.TotalTasks, rep.BudgetUsed.TokensUsed, rep.BudgetUsed.IterationsUsed)
	if len(rep.PausedCheckpoints) > 0 {
		fmt.Fprintf(os.Stdout, "paused checkpoints: %s\n", strings.Join(rep.PausedCheckpoints, ", "))
	}
	if rep.ValidationState != "" {
		fmt.Fprintf(os.Stdout, "validation: %s\n", rep.ValidationState)
	}
	for _, a := range rep.Assumptions {
		fmt.Fprintf(os.Stdout, "assumption: %s\n", a)
	}
	for _, d := range rep.Deviations {
		fmt.Fprintf(os.Stdout, "deviation: %s\n", d)
	}
	return nil
}

// daemonHint enriches connectivity failures with the actionable fix.
func daemonHint(err error) error {
	if errors.Is(err, client.ErrDaemonNotRunning) {
		return fmt.Errorf("%w\nhint: start the daemon first with 'forge serve'", err)
	}
	return err
}
