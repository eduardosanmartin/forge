package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/run"
)

// RunExecution lifecycle states (RF-11 daemon migration, Fase 1 of
// hojaDeRuta-multiagente.md). This is the daemon's OWN bookkeeping enum —
// same relationship Job's JobRunning/JobDone/... has to a plain agent turn
// — not a reuse of run.Report.Status, though most values map 1:1.
//
// RunPausedCheckpoint and RunKilled are TERMINAL here: the goroutine has
// already exited by the time either is set, exactly like a CLI-driven run
// pausing today (`forge run --manifest ... --resume` starts a fresh
// process). Fase 2 of the roadmap is where a checkpoint pause becomes a
// live, in-memory block on a channel that an external approval unblocks
// without a new goroutine — Fase 1 deliberately does not get ahead of that;
// ResumeRun (below) is Fase 1's own way to continue a paused run, mirroring
// Runner.Resume() exactly as the CLI already uses it.
const (
	RunRunning          = "running"
	RunPausedCheckpoint = "paused_checkpoint"
	RunDone             = "done"
	RunFailed           = "failed"
	RunKilled           = "killed"
	RunCanceled         = "canceled"
)

// RunExecution tracks one daemon-hosted manifest run.
type RunExecution struct {
	ID        string // manifest RunID
	SessionID string // isolated execution session backing this run's task turns
	CreatedAt int64
	UpdatedAt int64

	mu          sync.Mutex
	status      string
	currentTask string
	tokensUsed  int
	iterUsed    int
	err         string
	report      *run.Report
	cancel      context.CancelFunc
	done        chan struct{}
}

// RunResult is a thread-safe snapshot of a RunExecution — same shape/purpose
// as JobResult for Job.
type RunResult struct {
	ID          string
	SessionID   string
	Status      string
	CurrentTask string
	TokensUsed  int
	IterUsed    int
	Error       string
	CreatedAt   int64
	UpdatedAt   int64
	Report      *run.Report
}

// isRunActive reports whether id currently has a live goroutine (status
// RunRunning). A terminal entry (done/failed/killed/canceled/paused) never
// blocks starting fresh — StartRun/ResumeRun overwrite it in m.runs — only
// a genuinely in-flight run does, since two goroutines racing on the same
// run_id would corrupt its shared state.json.
func (m *SessionManager) isRunActive(id string) bool {
	res, ok := m.GetRun(id)
	return ok && res.Status == RunRunning
}

func (r *RunExecution) snapshot() RunResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunResult{
		ID:          r.ID,
		SessionID:   r.SessionID,
		Status:      r.status,
		CurrentTask: r.currentTask,
		TokensUsed:  r.tokensUsed,
		IterUsed:    r.iterUsed,
		Error:       r.err,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		Report:      r.report,
	}
}

// run helpers on SessionManager

// StartRun validates mani, creates its isolated execution session (skipped
// for dry_run, matching internal/cli/run.go's runManifest), and launches
// Runner.Run() in a goroutine — this method returns as soon as the run is
// registered and running, not when it finishes (same detached-execution
// shape as ExecuteTurn's Job queue, RF-1.4). decompose mirrors the CLI's
// --decompose flag.
func (m *SessionManager) StartRun(ctx context.Context, mani *run.Manifest, stateDir string, decompose bool) (*RunExecution, error) {
	if err := mani.ValidateAgainstSensitivity(m.cfg); err != nil {
		return nil, fmt.Errorf("sensitivity ceiling rejected manifest: %w", err)
	}
	if m.isRunActive(mani.RunID) {
		return nil, fmt.Errorf("run %q already active", mani.RunID)
	}

	var sessID string
	if mani.Mode != run.ModeDryRun {
		sess, err := m.CreateSession(ctx, map[string]any{
			"source":      "run_manifest",
			"run_id":      mani.RunID,
			"mode":        mani.Mode,
			"work_branch": mani.Git.WorkBranch,
		})
		if err != nil {
			return nil, fmt.Errorf("create run session: %w", err)
		}
		sessID = sess.ID
	}

	runner := m.newDaemonManifestRunner(mani, stateDir, sessID)
	if decompose {
		runner.Decompose = true
		runner.Decomposer = m.manifestDecomposer()
	}
	return m.launchRun(mani.RunID, sessID, func(rctx context.Context) (*run.Report, error) {
		return runner.Run(rctx)
	}), nil
}

// ResumeRun continues a run interrupted by a crash, disconnect, or HITL
// pause: reloads persisted state for mani.RunID, reuses its original
// session so task turns keep the conversational context already built up,
// and calls Runner.Resume() in a goroutine. Mirrors the CLI's --resume path
// (internal/cli/run.go).
func (m *SessionManager) ResumeRun(ctx context.Context, mani *run.Manifest, stateDir string) (*RunExecution, error) {
	if mani.Mode == run.ModeDryRun {
		return nil, fmt.Errorf("--resume is not valid with mode dry_run: dry runs execute nothing and persist no state to resume from")
	}
	if m.isRunActive(mani.RunID) {
		return nil, fmt.Errorf("run %q already active", mani.RunID)
	}
	prev, err := run.LoadState(stateDir, mani.RunID)
	if err != nil {
		return nil, fmt.Errorf("load previous state for run %q: %w", mani.RunID, err)
	}
	if prev.SessionID == "" {
		return nil, fmt.Errorf("run %q has no session recorded in its persisted state, cannot continue its conversation", mani.RunID)
	}

	runner := m.newDaemonManifestRunner(mani, stateDir, prev.SessionID)
	_ = ctx // reserved: session existence isn't re-checked here, same as CLI's --resume today
	return m.launchRun(mani.RunID, prev.SessionID, func(rctx context.Context) (*run.Report, error) {
		return runner.Resume(rctx)
	}), nil
}

// newDaemonManifestRunner builds a Runner wired to run in-process inside the
// daemon: Executor/Decomposer call SessionManager methods directly (no RPC
// round trip to itself — see manifestExecutor/manifestDecomposer), and
// OnCheckpoint/OnProgress log structurally instead of printing to a
// terminal, since no CLI process owns this run's stdout.
func (m *SessionManager) newDaemonManifestRunner(mani *run.Manifest, stateDir, sessionID string) *run.Runner {
	r := &run.Runner{
		Manifest:  mani,
		Config:    m.cfg,
		StateDir:  stateDir,
		SessionID: sessionID,
	}
	if mani.Mode != run.ModeDryRun {
		r.Executor = m.manifestExecutor(sessionID)
	}
	r.OnCheckpoint = func(cp run.Checkpoint, _ *run.RunState) (bool, error) {
		m.logger.Info("run checkpoint reached", "run_id", mani.RunID, "checkpoint", cp.ID, "trigger", cp.Trigger)
		// Fase 1: same "pause, persist state, exit" contract the CLI already
		// uses — a required checkpoint always returns false here; Runner
		// persists RunState and Run()/Resume() returns a StatusPaused
		// report. ResumeRun (above) is how the run continues once approved
		// (a fresh goroutine reusing the same session). Fase 2 replaces
		// this with a real live block on a channel.
		return false, nil
	}
	r.OnProgress = func(ev run.ProgressEvent) {
		m.setRunProgress(mani.RunID, ev)
	}
	return r
}

// setRunProgress updates a live RunExecution's current-task/budget fields
// from a run.ProgressEvent — the same event shape internal/cli/run.go's
// printManifestProgress already consumes for terminal output, here kept as
// queryable in-memory state instead (GetRun/ListRuns).
func (m *SessionManager) setRunProgress(runID string, ev run.ProgressEvent) {
	m.runsMu.RLock()
	exec, ok := m.runs[runID]
	m.runsMu.RUnlock()
	if !ok {
		return
	}
	exec.mu.Lock()
	exec.currentTask = ev.TaskID
	exec.tokensUsed = ev.TokensUsed
	exec.iterUsed = ev.IterationsUsed
	exec.UpdatedAt = time.Now().UnixMilli()
	exec.mu.Unlock()
	m.logger.Debug("run progress", "run_id", runID, "phase", ev.Phase,
		"task_id", ev.TaskID, "task_index", ev.TaskIndex, "total_tasks", ev.TotalTasks)
}

// launchRun registers a new RunExecution and starts call in a goroutine,
// returning immediately — call is Runner.Run or Runner.Resume, injected so
// StartRun/ResumeRun share this bookkeeping without duplicating it.
func (m *SessionManager) launchRun(runID, sessionID string, call func(context.Context) (*run.Report, error)) *RunExecution {
	rctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UnixMilli()
	exec := &RunExecution{
		ID:        runID,
		SessionID: sessionID,
		CreatedAt: now,
		UpdatedAt: now,
		status:    RunRunning,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.runsMu.Lock()
	if m.runs == nil {
		m.runs = make(map[string]*RunExecution)
	}
	m.runs[runID] = exec
	m.runsMu.Unlock()

	go func() {
		report, err := call(rctx)
		m.finishRun(exec, report, err, rctx.Err())
		m.logger.Info("run finished", "run_id", runID, "status", exec.snapshot().Status)
	}()

	return exec
}

// finishRun records the terminal outcome of a run's goroutine. A no-op if
// the run was already finalized externally (CancelRun racing the goroutine
// itself) — same guard Job.finishJob uses.
func (m *SessionManager) finishRun(exec *RunExecution, report *run.Report, err error, ctxErr error) {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.status != RunRunning {
		return
	}
	exec.UpdatedAt = time.Now().UnixMilli()
	switch {
	case report != nil:
		// A report means Run()/Resume() reached a real terminal state.
		// Its Status is authoritative EVEN WHEN err is also non-nil:
		// pauseReport/killedReport/failReport (internal/run/runner.go) all
		// deliberately return (report, err) together — e.g. pauseReport's
		// err reads "paused at checkpoint ... — awaiting approval", a
		// human-readable reason string for CLI/RPC callers, not a real
		// failure. Checking err first would misclassify every HITL pause
		// and every hard-budget kill as RunFailed.
		exec.report = report
		exec.tokensUsed = report.BudgetUsed.TokensUsed
		exec.iterUsed = report.BudgetUsed.IterationsUsed
		switch report.Status {
		case run.StatusCompleted:
			exec.status = RunDone
		case run.StatusPaused:
			exec.status = RunPausedCheckpoint
		case run.StatusKilled:
			exec.status = RunKilled
		default:
			exec.status = RunFailed
		}
		if err != nil {
			exec.err = err.Error()
		}
	case ctxErr == context.Canceled:
		exec.status = RunCanceled
		if err != nil {
			exec.err = err.Error()
		}
	case err != nil:
		exec.status = RunFailed
		exec.err = err.Error()
	default:
		exec.status = RunFailed
		exec.err = "run produced no report"
	}
	select {
	case <-exec.done:
	default:
		close(exec.done)
	}
}

// manifestExecutor is the daemon-internal equivalent of
// client.ManifestExecutor: runs each task goal as one agent turn, in
// process (SessionManager method calls, no RPC round trip to itself).
// Reuses summarizeTurn/messageToResult — the exact same tool-trace/usage
// extraction the run.* RPC handlers use — so a daemon-hosted run's
// ExecResult matches the CLI-driven path byte for byte.
func (m *SessionManager) manifestExecutor(sessionID string) run.Executor {
	return func(ctx context.Context, task run.Task) (run.ExecResult, error) {
		msgs, err := m.ExecuteTurnWithModelHint(ctx, sessionID, task.Goal, task.ModelHint)
		if err != nil {
			return run.ExecResult{}, fmt.Errorf("execute task %s: %w", task.ID, err)
		}
		result := ExecuteTurnResult{Messages: make([]MessageResult, len(msgs))}
		for i, msg := range msgs {
			result.Messages[i] = messageToResult(msg)
		}
		summarizeTurn(&result)
		tokens := 0
		if result.Usage != nil {
			tokens = result.Usage.TotalTokens
		}
		iters := 1
		if len(result.ToolTrace) > 0 {
			iters = len(result.ToolTrace) + 1
		}
		toolCalls := make([]string, 0, len(result.ToolTrace))
		for _, tr := range result.ToolTrace {
			toolCalls = append(toolCalls, tr.Name)
		}
		return run.ExecResult{Tokens: tokens, Iterations: iters, ToolCalls: toolCalls}, nil
	}
}

// manifestDecomposer is the daemon-internal equivalent of
// client.ManifestDecomposer: a dedicated, throwaway no_tools session per
// call, kept separate from the run's own session so decomposition chatter
// never becomes part of the context every subsequent task turn sees.
func (m *SessionManager) manifestDecomposer() run.Decomposer {
	return func(ctx context.Context, goal, spec string) ([]run.Task, error) {
		sess, err := m.CreateSession(ctx, map[string]any{
			"source":   "run_manifest_decompose",
			"no_tools": true,
		})
		if err != nil {
			return nil, fmt.Errorf("create decomposition session: %w", err)
		}
		msgs, err := m.ExecuteTurn(ctx, sess.ID, run.BuildDecompositionPrompt(goal, spec))
		if err != nil {
			return nil, fmt.Errorf("decomposition turn: %w", err)
		}
		var finalContent string
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "assistant" {
				finalContent = msgs[i].Content
				break
			}
		}
		return run.ParseDecomposedTasks(finalContent)
	}
}

// GetRun returns a snapshot of a run by ID.
func (m *SessionManager) GetRun(id string) (RunResult, bool) {
	m.runsMu.RLock()
	exec, ok := m.runs[id]
	m.runsMu.RUnlock()
	if !ok {
		return RunResult{}, false
	}
	return exec.snapshot(), true
}

// ListRuns returns a snapshot of all runs sorted by CreatedAt descending.
func (m *SessionManager) ListRuns() []RunResult {
	m.runsMu.RLock()
	defer m.runsMu.RUnlock()
	out := make([]RunResult, 0, len(m.runs))
	for _, e := range m.runs {
		out = append(out, e.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// CancelRun cancels a running run by ID — same optimistic-set-then-let-the-
// goroutine-observe-it pattern as Job.CancelJob: the context is canceled,
// status is set to RunCanceled immediately (so a concurrent GetRun sees it
// right away) rather than waiting for the goroutine to actually unwind.
func (m *SessionManager) CancelRun(id string) (RunResult, error) {
	m.runsMu.RLock()
	exec, ok := m.runs[id]
	m.runsMu.RUnlock()
	if !ok {
		return RunResult{}, fmt.Errorf("run not found: %s", id)
	}
	exec.mu.Lock()
	running := exec.status == RunRunning
	cancel := exec.cancel
	exec.mu.Unlock()
	if !running {
		return exec.snapshot(), nil
	}
	if cancel != nil {
		cancel()
	}
	exec.mu.Lock()
	if exec.status == RunRunning {
		exec.status = RunCanceled
		exec.err = "canceled by user"
		exec.UpdatedAt = time.Now().UnixMilli()
		select {
		case <-exec.done:
		default:
			close(exec.done)
		}
	}
	exec.mu.Unlock()
	return exec.snapshot(), nil
}
