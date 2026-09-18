package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
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
		decompose        bool
		logActivity      bool
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
			"                     recomputes the hash chain and reports whether it's intact.\n" +
			"  --decompose        When the manifest declares no explicit \"tasks\", ask the daemon's\n" +
			"                     default model to break \"goal\" (+ \"spec\" when present) into an atomic\n" +
			"                     task list before running — each task sized for a single small turn\n" +
			"                     (see sugerenciasDeClaude.md §5). The proposal is written to\n" +
			"                     --state-dir/.forge/runs/<run_id>/tasks.decomposed.json and gated by\n" +
			"                     the manifest's after_spec_decomposition checkpoint when declared.\n" +
			"                     Requires --manifest; refuses a manifest that already has \"tasks\".\n" +
			"  --log              Mirror every task/tool/checkpoint activity line into\n" +
			"                     --state-dir/.forge/runs/<run_id>/activity.log, in addition to the\n" +
			"                     terminal (or instead of it, for lines --json otherwise suppresses) —\n" +
			"                     a persistent record of what a long autonomous run actually did.\n" +
			"                     Appended across --resume invocations of the same run_id, never\n" +
			"                     truncated.",
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
				if decompose && resume {
					return usageErrorf("--decompose and --resume are mutually exclusive — a resumed run reuses its original task list, decomposed or not")
				}
				if decompose && verifyAudit {
					return usageErrorf("--decompose and --verify-audit are mutually exclusive")
				}
				return nil
			}
			if resume {
				return usageErrorf("--resume requires --manifest")
			}
			if verifyAudit {
				return usageErrorf("--verify-audit requires --manifest")
			}
			if decompose {
				return usageErrorf("--decompose requires --manifest")
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
				// runManifest silences cmd's error print itself, but only once
				// it actually has a Report to have printed guidance for — an
				// early failure (no daemon, bad manifest, sensitivity
				// rejection...) never reaches that point and must still get
				// cobra's normal "Error: %s" (e.g. the "run forge serve" hint),
				// exactly as before this change.
				return runManifest(cmd, manifestPath, jsonOut, autoYes, stateDir, resume, decompose, logActivity)
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
	cmd.Flags().BoolVar(&decompose, "decompose", false, "ask the default model to break the manifest's goal into an atomic task list before running (requires an empty \"tasks\" in the manifest)")
	cmd.Flags().BoolVar(&logActivity, "log", false, "mirror activity (progress, tool calls, checkpoints, report) into --state-dir/.forge/runs/<run_id>/activity.log")
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

// pollSubagentsInterval is how often pollSubagents polls session.list.
// Deliberately not tighter than a few seconds: it is a real RPC round trip,
// and subagent trees change slowly relative to LLM turn latency.
const pollSubagentsInterval = 4 * time.Second

// pollSubagents polls session.list every pollSubagentsInterval until ctx is
// done, printing a summary whenever the set of direct subagents parented at
// sessionID changes (a new one appears, or an existing one's message count
// moves) — the same metadata `forge subagents list` already reads
// (session.Metadata["subagent"]/["subagent_parent"]), just watched live
// instead of queried once on demand. A poll failure is swallowed (best
// effort, must never interrupt or spam the run over a transient RPC hiccup).
func pollSubagents(ctx context.Context, cl *client.Client, sessionID string, out io.Writer) {
	ticker := time.NewTicker(pollSubagentsInterval)
	defer ticker.Stop()
	seen := map[string]int{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		res, err := cl.ListSessions(ctx, 200, 0)
		if err != nil {
			continue
		}
		current := subagentSnapshot(res.Sessions, sessionID)
		if msg, changed := formatSubagentSnapshot(seen, current); changed {
			fmt.Fprint(out, msg)
		}
		seen = current
	}
}

// subagentSnapshot extracts, from one session.list response, the direct
// subagents parented at sessionID — the same metadata `forge subagents
// list` reads (session.Metadata["subagent"]/["subagent_parent"]) — as
// childSessionID -> current message count.
func subagentSnapshot(sessions []daemon.SessionResult, sessionID string) map[string]int {
	current := map[string]int{}
	for _, s := range sessions {
		if s.Metadata == nil {
			continue
		}
		isSub, _ := s.Metadata["subagent"].(bool)
		if !isSub {
			continue
		}
		parentID, _ := s.Metadata["subagent_parent"].(string)
		if parentID != sessionID {
			continue
		}
		current[s.ID] = s.MessageCount
	}
	return current
}

// formatSubagentSnapshot compares two consecutive subagentSnapshot results
// and reports whether anything changed (a subagent appeared/disappeared, or
// an existing one's message count moved) plus, when it did, the summary
// line(s) to print. Returns changed=false (and an empty message) for "no
// subagents at all" so a run with none stays silent instead of repeating
// "0 activos" every poll interval.
func formatSubagentSnapshot(prev, current map[string]int) (msg string, changed bool) {
	if len(current) == 0 {
		return "", false
	}
	changed = len(current) != len(prev)
	if !changed {
		for id, msgs := range current {
			if prev[id] != msgs {
				changed = true
				break
			}
		}
	}
	if !changed {
		return "", false
	}
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "[subagentes] %d activo(s) bajo esta corrida:\n", len(current))
	for _, id := range ids {
		short := id
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Fprintf(&b, "  - %s: %d mensajes\n", short, current[id])
	}
	return b.String(), true
}

// printToolCallTick renders one live daemon.MethodToolCallEvent for a
// manifest task's turn — the same "-> tool(...)" / "<- ok"/"<- error" shape
// writeHumanResult prints after the fact for a single-prompt turn, but LIVE
// while the task's turn is still in flight (see watchManifestEvents). The
// event payload carries no argument preview (daemon.ToolCallEventPayload
// has none), only name/status.
func printToolCallTick(w io.Writer, ev daemon.ToolCallEventPayload) {
	switch ev.Status {
	case "started":
		fmt.Fprintf(w, "  -> %s\n", ev.Name)
	case "finished":
		fmt.Fprintln(w, "  <- ok")
	case "error":
		fmt.Fprintf(w, "  <- error: %s\n", ev.Error)
	}
}

// activityWriter combines a terminal target (nil when the terminal should
// stay quiet for this line — e.g. --json mode) with an optional --log file,
// so activity still lands in the file even when the terminal is suppressed.
// Returns nil only when neither target applies (caller skips entirely).
func activityWriter(terminal, logFile io.Writer) io.Writer {
	switch {
	case terminal == nil && logFile == nil:
		return nil
	case terminal == nil:
		return logFile
	case logFile == nil:
		return terminal
	default:
		return io.MultiWriter(terminal, logFile)
	}
}

// openActivityLog creates (or appends to) --state-dir/.forge/runs/<run_id>/
// activity.log when --log is set, colocated with the state.json/report.json
// this same run already persists there. Appends rather than truncates so a
// resumed run's log reads as one continuous timeline across invocations.
func openActivityLog(stateDir, runID string, verb, manifestPath string) (*os.File, error) {
	dir := filepath.Join(stateDir, ".forge", "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("--log: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "activity.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("--log: open %s: %w", path, err)
	}
	fmt.Fprintf(f, "\n=== forge %s --manifest %s (run_id=%s) — %s ===\n", verb, manifestPath, runID, time.Now().Format(time.RFC3339))
	return f, nil
}

func runManifest(cmd *cobra.Command, manifestPath string, jsonOut, autoYes bool, stateDir string, resume, decompose, logActivity bool) error {
	ctx := cmd.Context()
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
	if decompose && len(mani.Tasks) > 0 {
		return &UsageError{Err: fmt.Errorf("--decompose is redundant: manifest %q already declares %d task(s)", manifestPath, len(mani.Tasks))}
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

	var logFile io.Writer
	if logActivity {
		verb := "run"
		if resume {
			verb = "resume"
		}
		f, lErr := openActivityLog(stateDir, mani.RunID, verb, manifestPath)
		if lErr != nil {
			return lErr
		}
		defer f.Close()
		logFile = f
	}

	if resume {
		// RF-11.8 fast-fail: confirm locally that there's something to
		// resume BEFORE even trying to reach a daemon — run.resume
		// (hojaDeRuta-multiagente.md Fase 5) re-validates the exact same
		// state.json server-side, but a clear "nothing to resume" error
		// shouldn't require a daemon to be running at all, and this keeps
		// that property exactly as it always worked. Fase 3's ResumeRun
		// assumes the SAME state-dir/cwd convention as the CLI (the daemon
		// never reads the client's filesystem for anything else, but
		// --state-dir is a path meaningful only relative to whichever
		// process resolves it — already true for every run.* RPC since
		// Fase 1, not a new constraint from this rewrite).
		prev, lErr := run.LoadState(stateDir, mani.RunID)
		if lErr != nil {
			return fmt.Errorf("--resume: load previous state for run %q: %w", mani.RunID, lErr)
		}
		if prev.SessionID == "" {
			return fmt.Errorf("--resume: run %q has no session recorded in its persisted state, cannot continue its conversation", mani.RunID)
		}
	}

	// Dry-run needs no daemon and no LLM — UNLESS decomposition was
	// requested: that needs both to ask the model for a task list, even
	// though the resulting plan is only previewed, never executed
	// (Runner.Run decomposes BEFORE its own dry_run short-circuit). This is
	// the ONE manifest path Fase 5 deliberately keeps running locally
	// in-process (Runner.Run, no RPC at all) instead of through run.start:
	// routing it through the daemon would newly REQUIRE one to be running
	// for a mode that has never needed it. Runner.Run for dry_run always
	// short-circuits to dryRunReport() before ever reaching execute() — the
	// only place OnCheckpoint/OnProgress/Executor are used — so this needs
	// none of them (Fase 6: the old newManifestRunner wired all three
	// unconditionally, but they were unreachable dead code at this, the
	// only remaining call site, once Fase 5 moved every other manifest path
	// onto run.start/run.resume).
	if mani.Mode == run.ModeDryRun && !decompose {
		r := &run.Runner{Manifest: mani, Config: cfg, StateDir: stateDir}
		rep, _ := r.Run(ctx)
		return writeManifestReport(activityWriter(os.Stdout, logFile), rep, jsonOut)
	}

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	// Subscribe to events BEFORE run.start/run.resume, not after: cl.Events
	// has no history/replay (a notification published before a connection
	// subscribes is simply dispatched to whichever subscribers exist AT
	// THAT INSTANT — see internal/client/client.go's Events). A
	// fast-completing task, or one that reaches its first checkpoint almost
	// immediately, can publish run.checkpoint.event before run.start's RPC
	// round trip even returns; subscribing only afterward would silently
	// miss it forever and leave waitForRunCompletion polling a paused run
	// nobody ever approved or declined. The channel buffers (64 deep) until
	// driveManifestRun's watcher goroutine actually starts reading it below.
	eventsCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()
	events, evErr := cl.Events(eventsCtx)

	var res *daemon.RunResult
	if resume {
		res, err = callRunResume(ctx, cl, mani, stateDir)
	} else {
		res, err = callRunStart(ctx, cl, mani, stateDir, decompose)
	}
	if err != nil {
		return err
	}

	rep, runErr := driveManifestRun(ctx, eventsCtx, cl, mani, res, events, evErr, jsonOut, autoYes, logFile, cancelEvents)
	// Always report, even when paused/killed.
	if rep != nil {
		if wErr := writeManifestReport(activityWriter(os.Stdout, logFile), rep, jsonOut); wErr != nil {
			return wErr
		}
		if !jsonOut {
			printManifestGuidance(activityWriter(os.Stdout, logFile), rep, manifestPath)
		}
		// We reached a real Report and already printed our own guidance for
		// it above — cobra's generic "Error: %s" on top would only repeat
		// (or, for a HITL pause, misrepresent) what writeManifestReport and
		// printManifestGuidance already said. Every EARLIER failure (no
		// daemon, bad manifest, sensitivity rejection...) returns before
		// this point and keeps cobra's normal error printing.
		cmd.SilenceErrors = true
	}
	return runErr
}

// callRunStart sends the parsed manifest (never the path — the daemon
// never reads the client's filesystem, hojaDeRuta-multiagente.md Fase 0/3)
// to run.start and returns the initial snapshot (SessionID, in particular,
// needed to filter tool.call.event next).
func callRunStart(ctx context.Context, cl *client.Client, mani *run.Manifest, stateDir string, decompose bool) (*daemon.RunResult, error) {
	var res daemon.RunResult
	if err := cl.Call(ctx, daemon.MethodRunStart, daemon.RunStartParams{Manifest: *mani, StateDir: stateDir, Decompose: decompose}, &res); err != nil {
		return nil, fmt.Errorf("run.start: %w", err)
	}
	return &res, nil
}

// callRunResume is callRunStart's --resume counterpart (run.resume).
func callRunResume(ctx context.Context, cl *client.Client, mani *run.Manifest, stateDir string) (*daemon.RunResult, error) {
	var res daemon.RunResult
	if err := cl.Call(ctx, daemon.MethodRunResume, daemon.RunResumeParams{Manifest: *mani, StateDir: stateDir}, &res); err != nil {
		return nil, fmt.Errorf("--resume: %w", err)
	}
	return &res, nil
}

// runStatusPollInterval paces waitForRunCompletion's run.status polling —
// purely a completion detector (no RPC notification signals "the whole
// manifest finished", see hojaDeRuta-multiagente.md Fase 5 notes), it
// never affects what gets PRINTED (that's entirely event-driven, see
// watchManifestEvents), only how promptly the CLI notices the run is done
// and exits.
const runStatusPollInterval = 300 * time.Millisecond

// runEventDrainGrace is a short pause between waitForRunCompletion
// returning and canceling the event watcher: run.status (a synchronous
// poll) and run.progress.event/run.checkpoint.event (async notifications)
// travel over independent channels, so the very last progress line for a
// just-finished run can still be in flight when the completion poll
// already sees Report != nil. This narrows that window; it does not close
// it — a documented, accepted limitation of a push-events + poll design
// (see driveManifestRun).
const runEventDrainGrace = 150 * time.Millisecond

// driveManifestRun replaces the old in-process `Runner.Run()`/`Resume()`
// blocking call (hojaDeRuta-multiagente.md Fase 5): it prints the exact
// same progress/HITL/tool-tick terminal lines those callbacks used to
// produce — printManifestProgress/printToolCallTick are reused UNCHANGED —
// but sourced from run.progress.event/run.checkpoint.event/tool.call.event
// notifications instead of direct Go callbacks, since the Runner itself now
// lives in the daemon (see internal/daemon/runs.go's newDaemonManifestRunner).
// Approving/declining a checkpoint calls run.approve_checkpoint the instant
// its event arrives, autoYes deciding the direction — the same immediate,
// non-interactive decision the old OnCheckpoint callback made synchronously
// (this command has never read stdin for HITL approval; a decline just
// pauses the run and the CLI reports that and exits, same as always).
func driveManifestRun(ctx, eventsCtx context.Context, cl *client.Client, mani *run.Manifest, initial *daemon.RunResult, events <-chan daemon.JSONRPCNotification, eventsErr error, jsonOut, autoYes bool, logFile io.Writer, cancelEvents context.CancelFunc) (*run.Report, error) {
	runID := mani.RunID
	sessionID := initial.SessionID

	var toolTickTerminal io.Writer
	if !jsonOut {
		toolTickTerminal = os.Stderr
	}
	ttw := activityWriter(toolTickTerminal, logFile)
	// Progress/HITL lines go to stderr unconditionally of --json (same
	// convention as before this rewrite: --json only constrains the final
	// stdout result, stderr stays human-readable).
	progressW := activityWriter(os.Stderr, logFile)

	if eventsErr == nil {
		// eventsCtx, not ctx: this goroutine must stop specifically when
		// cancelEvents() fires (below, and in runManifest's own defer) —
		// ctx (the whole command's context) only ends at process exit.
		go watchManifestEvents(eventsCtx, cl, runID, sessionID, events, progressW, ttw, autoYes)
	}

	// Nivel 2 observability: poll for subagents a task spawns (spawn_subagent
	// / RF-1.2) while the run is in flight. Unchanged mechanism from before
	// this rewrite — a CLI-side session.list poll, independent of where the
	// Runner itself executes — just fed sessionID from the RPC result
	// instead of a locally created session.
	if mani.Mode != run.ModeDryRun {
		var subagentTerminal io.Writer
		if !jsonOut {
			subagentTerminal = os.Stderr
		}
		if saw := activityWriter(subagentTerminal, logFile); saw != nil {
			pollCtx, pollCancel := context.WithCancel(ctx)
			defer pollCancel()
			go pollSubagents(pollCtx, cl, sessionID, saw)
		}
	}

	final, err := waitForRunCompletion(ctx, cl, runID)
	if err != nil {
		cancelEvents()
		return nil, err
	}
	time.Sleep(runEventDrainGrace)
	cancelEvents()
	return final.Report, errFromRunResult(final)
}

// watchManifestEvents is driveManifestRun's event loop, run in its own
// goroutine for the run's whole lifetime (canceled via eventsCtx). Every
// notification this connection receives is filtered here — cl.Events never
// scopes a subscription server-side, so every connected client gets every
// broadcast notification and filters client-side (same convention the
// TUI's event handling already relies on).
func watchManifestEvents(ctx context.Context, cl *client.Client, runID, sessionID string, events <-chan daemon.JSONRPCNotification, progressW, ttw io.Writer, autoYes bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case notif, ok := <-events:
			if !ok {
				return
			}
			switch notif.Method {
			case daemon.MethodRunProgressEvent:
				if progressW == nil {
					continue
				}
				var payload daemon.RunProgressEventPayload
				if json.Unmarshal(notif.Params, &payload) != nil || payload.RunID != runID {
					continue
				}
				printManifestProgress(progressW, progressEventFromPayload(payload))
			case daemon.MethodRunCheckpointEvent:
				var payload daemon.RunCheckpointEventPayload
				if json.Unmarshal(notif.Params, &payload) != nil || payload.RunID != runID {
					continue
				}
				handleCheckpointEvent(ctx, cl, progressW, runID, payload, autoYes)
			case daemon.MethodToolCallEvent:
				if ttw == nil {
					continue
				}
				var payload daemon.ToolCallEventPayload
				if json.Unmarshal(notif.Params, &payload) != nil || payload.SessionID != sessionID {
					continue
				}
				printToolCallTick(ttw, payload)
			}
		}
	}
}

// progressEventFromPayload reconstructs a run.ProgressEvent from its wire
// payload so printManifestProgress's exact format strings (including %v on
// Err) stay the single source of truth for this output — no duplicated
// formatting logic between the old in-process path and this one.
func progressEventFromPayload(p daemon.RunProgressEventPayload) run.ProgressEvent {
	var errVal error
	if p.Error != "" {
		errVal = errors.New(p.Error)
	}
	return run.ProgressEvent{
		Phase: run.ProgressPhase(p.Phase), TaskID: p.TaskID, TaskIndex: p.TaskIndex, TotalTasks: p.TotalTasks,
		Attempt: p.Attempt, MaxRetries: p.MaxRetries,
		TokensUsed: p.TokensUsed, IterationsUsed: p.IterationsUsed, Err: errVal,
	}
}

// handleCheckpointEvent reproduces the exact HITL lines the old in-process
// OnCheckpoint callback (removed in Fase 6 — see Runner.Run's dry_run call
// site above) used to print synchronously, then submits the same immediate
// decision via run.approve_checkpoint. A
// failure submitting the decision is a genuinely NEW failure mode this
// architecture introduces (the old in-process callback could never fail to
// deliver its own return value) — surfaced to stderr rather than silently
// leaving the run parked forever with no visible explanation.
func handleCheckpointEvent(ctx context.Context, cl *client.Client, w io.Writer, runID string, payload daemon.RunCheckpointEventPayload, autoYes bool) {
	if w != nil {
		if autoYes {
			fmt.Fprintf(w, "[HITL auto-approved] %s (%s)\n", payload.Checkpoint, payload.Trigger)
		} else {
			fmt.Fprintf(w, "[HITL] checkpoint %q (%s) requires approval — run paused (re-run with --yes to auto-approve)\n", payload.Checkpoint, payload.Trigger)
		}
	}
	var res daemon.RunResult
	if err := cl.Call(ctx, daemon.MethodRunApproveCheckpoint, daemon.RunApproveCheckpointParams{RunID: runID, Approved: autoYes}, &res); err != nil {
		fmt.Fprintf(os.Stderr, "[HITL] failed to submit checkpoint decision for run %s: %v\n", runID, err)
	}
}

// waitForRunCompletion polls run.status until the run reaches a genuinely
// terminal state — Report != nil, the same signal
// internal/daemon/runs_test.go's isRunTerminal uses, since RunPausedCheckpoint
// is NOT terminal while still live-blocked waiting on a decision (Report is
// nil until the goroutine actually returns). A transient poll error is
// swallowed and retried (matches pollSubagents' own best-effort convention)
// unless ctx itself is done, which is the only way this returns early
// without a result.
func waitForRunCompletion(ctx context.Context, cl *client.Client, runID string) (*daemon.RunResult, error) {
	for {
		var res daemon.RunResult
		if err := cl.Call(ctx, daemon.MethodRunStatus, daemon.RunStatusParams{RunID: runID}, &res); err == nil && res.Report != nil {
			return &res, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(runStatusPollInterval):
		}
	}
}

// errFromRunResult reconstructs the same error Runner.Run()/Resume() used
// to return alongside its report (pauseReport/killedReport/failReport in
// internal/run/runner.go all deliberately return report+error together) —
// finishRun (internal/daemon/runs.go) already preserves that exact text in
// RunResult.Error, so this just turns "" back into nil.
func errFromRunResult(res *daemon.RunResult) error {
	if res.Error == "" {
		return nil
	}
	return errors.New(res.Error)
}

// printManifestGuidance prints the one human-facing line that used to be
// cobra's generic "Error: %s" (now silenced for manifest mode — see the run
// command's RunE). A HITL pause awaiting approval is the run working
// correctly, not a failure, so it gets a distinct, non-alarming message and
// the exact next command instead of a message indistinguishable from a
// genuine problem. Killed/failed runs keep their existing report content
// (already printed via writeManifestReport's deviation/assumption fields) —
// this only adds the specific next-step guidance for each case.
func printManifestGuidance(w io.Writer, rep *run.Report, manifestPath string) {
	switch rep.Status {
	case run.StatusCompleted:
		// No error was returned for this case; nothing more to say.
	case run.StatusPaused:
		retriesExhausted := false
		for _, cpID := range rep.PausedCheckpoints {
			if cpID == "implicit-retries-exhausted" {
				retriesExhausted = true
			}
		}
		if retriesExhausted {
			fmt.Fprintf(w, "\n⚠ Una tarea no se completó tras agotar los reintentos — revisá el motivo arriba.\n")
			fmt.Fprintf(w, "Para reintentarla (por ejemplo tras ajustar config/manifest): forge run --manifest %s --resume\n", manifestPath)
			fmt.Fprintf(w, "(Ojo: --resume SIN --yes reintenta la tarea; CON --yes en este checkpoint la marca como fallida en forma definitiva, no la reintenta.)\n")
		} else {
			fmt.Fprintf(w, "\n✓ Generación exitosa — %d/%d tareas completadas. Pausado en checkpoint %s, a la espera de aprobación (HITL).\n",
				len(rep.CompletedTasks), rep.TotalTasks, strings.Join(rep.PausedCheckpoints, ", "))
			fmt.Fprintf(w, "Para aprobar y continuar: forge run --manifest %s --resume --yes\n", manifestPath)
		}
	case run.StatusKilled:
		fmt.Fprintf(w, "\n✗ Corrida detenida por presupuesto agotado (RNF-8) — no es reanudable. Ajustá el presupuesto en el manifiesto y arrancá una corrida nueva.\n")
	case run.StatusFailed:
		fmt.Fprintf(w, "\n✗ La corrida terminó en error — ver el detalle arriba.\n")
	}
}

func loadManifest(path string) (*run.Manifest, error) {
	// Delegates to run.ParseFile so spec_ref resolution and validation stay in one place.
	return run.ParseFile(path)
}

// printManifestProgress renders one run.ProgressEvent as a single line.
// TokensUsed/IterationsUsed are the run's cumulative totals (not per-task)
// so this line doubles as a running budget readout — the same numbers a
// hard-wall kill (RNF-8) would report.
func printManifestProgress(w io.Writer, ev run.ProgressEvent) {
	switch ev.Phase {
	case run.ProgressTaskStart:
		fmt.Fprintf(w, "[%d/%d] %s: iniciando...\n", ev.TaskIndex, ev.TotalTasks, ev.TaskID)
	case run.ProgressTaskRetry:
		fmt.Fprintf(w, "[%d/%d] %s: intento %d/%d (falló: %v)\n",
			ev.TaskIndex, ev.TotalTasks, ev.TaskID, ev.Attempt+1, ev.MaxRetries+1, ev.Err)
	case run.ProgressTaskDone:
		fmt.Fprintf(w, "[%d/%d] %s: listo (presupuesto acumulado: %d tokens, %d iteraciones)\n",
			ev.TaskIndex, ev.TotalTasks, ev.TaskID, ev.TokensUsed, ev.IterationsUsed)
	case run.ProgressTaskFailed:
		fmt.Fprintf(w, "[%d/%d] %s: falló tras %d intentos: %v\n",
			ev.TaskIndex, ev.TotalTasks, ev.TaskID, ev.MaxRetries+1, ev.Err)
	}
}

func writeManifestReport(out io.Writer, rep *run.Report, jsonOut bool) error {
	if rep == nil {
		return nil
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "run", rep)
	}
	fmt.Fprintf(out, "run %s [%s] %s — %d/%d tasks, budget tokens %d iters %d\n",
		rep.RunID, rep.Mode, rep.Status, len(rep.CompletedTasks), rep.TotalTasks, rep.BudgetUsed.TokensUsed, rep.BudgetUsed.IterationsUsed)
	if len(rep.PausedCheckpoints) > 0 {
		fmt.Fprintf(out, "paused checkpoints: %s\n", strings.Join(rep.PausedCheckpoints, ", "))
	}
	if rep.ValidationState != "" {
		fmt.Fprintf(out, "validation: %s\n", rep.ValidationState)
	}
	for _, a := range rep.Assumptions {
		fmt.Fprintf(out, "assumption: %s\n", a)
	}
	for _, d := range rep.Deviations {
		fmt.Fprintf(out, "deviation: %s\n", d)
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
