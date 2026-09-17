package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/run"
)

// RunExecution lifecycle states (RF-11 daemon migration, Fase 1+2 of
// hojaDeRuta-multiagente.md). This is the daemon's OWN bookkeeping enum —
// same relationship Job's JobRunning/JobDone/... has to a plain agent turn
// — not a reuse of run.Report.Status, though most values map 1:1.
//
// RunPausedCheckpoint is a LIVE, non-terminal state as of Fase 2: the
// goroutine is still alive, blocked inside OnCheckpoint reading from
// checkpointCh (see newDaemonManifestRunner). ApproveRunCheckpoint
// unblocks it in place — no new goroutine, no session/context loss —
// which is strictly better than Fase 1's "pause ends the goroutine, a
// later ResumeRun starts a fresh one" behavior (still available and still
// correct: it's how a run recovers from an actual daemon restart, which
// necessarily kills every live goroutine regardless of this channel).
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

	mu                sync.Mutex
	status            string
	currentTask       string
	tokensUsed        int
	iterUsed          int
	err               string
	report            *run.Report
	ctx               context.Context // this run's own cancellation context, set once at launchRun before the goroutine starts
	cancel            context.CancelFunc
	done              chan struct{}
	pendingCheckpoint *run.Checkpoint // non-nil only while status == RunPausedCheckpoint
	checkpointCh      chan bool       // Fase 2: ApproveRunCheckpoint sends here; OnCheckpoint blocks reading it
	checkpointWaiting bool            // guards ApproveRunCheckpoint against a decision with nothing listening
}

// RunResult is a thread-safe snapshot of a RunExecution — same shape/purpose
// as JobResult for Job.
type RunResult struct {
	ID                string
	SessionID         string
	Status            string
	CurrentTask       string
	PendingCheckpoint *run.Checkpoint // set only when Status == RunPausedCheckpoint
	TokensUsed        int
	IterUsed          int
	Error             string
	CreatedAt         int64
	UpdatedAt         int64
	Report            *run.Report
}

// isRunActive reports whether id currently has a live goroutine — status
// RunRunning, or (Fase 2) RunPausedCheckpoint, since that goroutine is
// alive too, just blocked in OnCheckpoint. A truly terminal entry
// (done/failed/killed/canceled) never blocks starting fresh —
// StartRun/ResumeRun overwrite it in m.runs — only a genuinely in-flight
// or paused-in-place run does, since two goroutines racing on the same
// run_id would corrupt its shared state.json.
func (m *SessionManager) isRunActive(id string) bool {
	res, ok := m.GetRun(id)
	if !ok {
		return false
	}
	if res.Status == RunRunning {
		return true
	}
	// RunPausedCheckpoint is active ONLY while genuinely blocked live
	// (Report nil — Run()/Resume() has not returned yet). A DECLINED
	// checkpoint also reports RunPausedCheckpoint, but by then the
	// goroutine has exited and finishRun has set Report — that's Fase 1's
	// terminal pause, not a live one, and must not block a fresh
	// StartRun/ResumeRun.
	return res.Status == RunPausedCheckpoint && res.Report == nil
}

func (r *RunExecution) snapshot() RunResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunResult{
		ID:                r.ID,
		SessionID:         r.SessionID,
		Status:            r.status,
		CurrentTask:       r.currentTask,
		PendingCheckpoint: r.pendingCheckpoint,
		TokensUsed:        r.tokensUsed,
		IterUsed:          r.iterUsed,
		Error:             r.err,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
		Report:            r.report,
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
		m.runsMu.RLock()
		exec, ok := m.runs[mani.RunID]
		m.runsMu.RUnlock()
		if !ok {
			// Should never happen: launchRun registers exec before the
			// goroutine that could reach a checkpoint even starts. Never
			// block forever on a run nothing can find — decline safely,
			// same as Fase 1's unconditional false.
			return false, nil
		}

		// Fase 2: block on a fresh, per-checkpoint channel instead of
		// deciding synchronously — ApproveRunCheckpoint (below) delivers
		// the decision from any external caller, potentially much later,
		// without losing this run's goroutine/session/conversational
		// context (Fase 1's "pause ends the goroutine, ResumeRun starts a
		// fresh one" remains correct and available — it's how a run
		// recovers from an actual daemon restart, which kills this
		// goroutine regardless).
		ch := make(chan bool, 1)
		cpCopy := cp
		exec.mu.Lock()
		exec.status = RunPausedCheckpoint
		exec.pendingCheckpoint = &cpCopy
		exec.checkpointCh = ch
		exec.checkpointWaiting = true
		exec.UpdatedAt = time.Now().UnixMilli()
		exec.mu.Unlock()

		m.publishRunCheckpointEvent(mani.RunID, sessionID, cp, "checkpoint pending approval")

		select {
		case approved := <-ch:
			exec.mu.Lock()
			exec.checkpointWaiting = false
			exec.pendingCheckpoint = nil
			if approved {
				exec.status = RunRunning
			}
			exec.UpdatedAt = time.Now().UnixMilli()
			exec.mu.Unlock()
			return approved, nil
		case <-exec.ctx.Done():
			// CancelRun (or a genuine parent shutdown) fired while this run
			// was paused waiting for a decision — handleCheckpoint's caller
			// treats a non-nil error as a real failure (failReport), which
			// finishRun below maps back to RunCanceled via ctxErr, not
			// RunFailed (see finishRun's doc comment).
			exec.mu.Lock()
			exec.checkpointWaiting = false
			exec.pendingCheckpoint = nil
			exec.UpdatedAt = time.Now().UnixMilli()
			exec.mu.Unlock()
			return false, exec.ctx.Err()
		}
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
		ctx:       rctx,
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

// finishRun records the terminal outcome of a run's goroutine and is the
// SOLE closer of exec.done — called exactly once, from launchRun's
// goroutine, after call() truly returns. If the status was already
// finalized externally (CancelRun racing ahead of us — see its own doc
// comment) that verdict wins and is left untouched; either way, done is
// always closed here, so a caller waiting on it is guaranteed the
// goroutine has actually stopped, not just been asked to.
func (m *SessionManager) finishRun(exec *RunExecution, report *run.Report, err error, ctxErr error) {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.status == RunRunning || exec.status == RunPausedCheckpoint {
		exec.UpdatedAt = time.Now().UnixMilli()
		switch {
		case report != nil:
			// A report means Run()/Resume() reached a real terminal state.
			// Its Status is authoritative EVEN WHEN err is also non-nil:
			// pauseReport/killedReport/failReport (internal/run/runner.go)
			// all deliberately return (report, err) together — e.g.
			// pauseReport's err reads "paused at checkpoint ... — awaiting
			// approval", a human-readable reason string for CLI/RPC
			// callers, not a real failure. Checking err first would
			// misclassify every HITL pause and every hard-budget kill as
			// RunFailed.
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
	}
	// Defensive: never leave a stale pending checkpoint once the goroutine
	// has truly finished, whatever path got here.
	exec.pendingCheckpoint = nil
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

// publishRunCheckpointEvent broadcasts run.checkpoint.event the instant a
// run blocks on a required checkpoint (Fase 2 of hojaDeRuta-multiagente.md
// — no public RPC to approve it exists until Fase 3, but publishing this
// notification a phase early costs nothing since deltaPublisher already
// exists for tool.call.event/message.delta.event). Best-effort: a nil
// publisher (no client ever subscribed) or a marshal failure is silently
// skipped, same as onToolEvent's own convention.
func (m *SessionManager) publishRunCheckpointEvent(runID, sessionID string, cp run.Checkpoint, reason string) {
	m.mu.RLock()
	pub := m.deltaPublisher
	m.mu.RUnlock()
	if pub == nil {
		return
	}
	payload := RunCheckpointEventPayload{RunID: runID, SessionID: sessionID, Checkpoint: cp.ID, Trigger: cp.Trigger, Reason: reason}
	if notif, err := NewNotification(MethodRunCheckpointEvent, payload); err == nil {
		pub(sessionID, notif)
	}
}

// ApproveRunCheckpoint delivers a live decision to a run currently blocked
// on a checkpoint (Fase 2): approved=true continues execution in the SAME
// goroutine/session — no context lost, unlike Fase 1's "pause ends the
// goroutine, ResumeRun starts a fresh one" (still correct, still how a run
// recovers from an actual daemon restart). approved=false pauses it
// cleanly (Runner's own pauseReport path) and DOES end that goroutine —
// there is no reason to keep a declined run's goroutine alive; a later
// ResumeRun starts fresh and will reach the same checkpoint again. Returns
// an error if the run isn't currently waiting on a checkpoint at all
// (nothing to approve, or a decision was already delivered — checked and
// cleared atomically so two racing approvals can't both send).
func (m *SessionManager) ApproveRunCheckpoint(runID string, approved bool) (RunResult, error) {
	m.runsMu.RLock()
	exec, ok := m.runs[runID]
	m.runsMu.RUnlock()
	if !ok {
		return RunResult{}, fmt.Errorf("run not found: %s", runID)
	}
	exec.mu.Lock()
	if exec.status != RunPausedCheckpoint || !exec.checkpointWaiting {
		exec.mu.Unlock()
		return RunResult{}, fmt.Errorf("run %q is not currently awaiting a checkpoint decision", runID)
	}
	ch := exec.checkpointCh
	exec.checkpointWaiting = false
	exec.mu.Unlock()

	select {
	case ch <- approved:
	default:
		// Unreachable in practice: ch is buffered(1) and checkpointWaiting
		// guards against a second send — defensive, never blocks the caller.
	}
	return exec.snapshot(), nil
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

// CancelRun cancels a run by ID — running, or (Fase 2) paused live inside a
// checkpoint (its goroutine is still alive, blocked in OnCheckpoint, and
// exec.ctx.Done() unblocks it there too). Same optimistic-set pattern as
// Job.CancelJob: status flips to RunCanceled immediately (a concurrent
// GetRun sees it right away) rather than waiting for the goroutine to
// actually unwind — but unlike Job, this does NOT close exec.done itself;
// finishRun (called once, from the goroutine's own eventual completion) is
// the sole owner of that close, so a caller waiting on it always sees the
// goroutine has truly stopped, not just been asked to.
func (m *SessionManager) CancelRun(id string) (RunResult, error) {
	m.runsMu.RLock()
	exec, ok := m.runs[id]
	m.runsMu.RUnlock()
	if !ok {
		return RunResult{}, fmt.Errorf("run not found: %s", id)
	}
	exec.mu.Lock()
	cancelable := exec.status == RunRunning || exec.status == RunPausedCheckpoint
	cancel := exec.cancel
	if cancelable {
		exec.status = RunCanceled
		exec.err = "canceled by user"
		exec.pendingCheckpoint = nil
		exec.checkpointWaiting = false
		exec.UpdatedAt = time.Now().UnixMilli()
	}
	exec.mu.Unlock()
	if cancelable && cancel != nil {
		cancel()
	}
	return exec.snapshot(), nil
}
