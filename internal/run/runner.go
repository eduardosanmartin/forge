package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
)

// ExecResult is the outcome of one task execution, including the actual
// tool calls observed during the turn. ToolCalls may be nil when the
// executor cannot report tool records (legacy path); callers must treat
// nil as "unavailable" and fall back to heuristics, while an empty slice
// means "no tools executed".
type ExecResult struct {
	Tokens     int
	Iterations int
	ToolCalls  []string
}

// Executor executes one task goal and returns its result.
// Implementations may call agent.ExecuteTurn via daemon or a mock in tests.
type Executor func(ctx context.Context, task Task) (ExecResult, error)

// Decomposer proposes a task breakdown from the manifest's goal and spec
// text, called once before the task loop when the manifest declares no
// explicit tasks and Runner.Decompose is true. Implementations are expected
// to ask an LLM for a JSON task list and parse it (see
// client.ManifestDecomposer) — the runner never invents tasks on its own,
// and Manifest.EffectiveTasks()'s single-task fallback still applies when
// Decompose is false or Decomposer is nil.
type Decomposer func(ctx context.Context, goal, spec string) ([]Task, error)

// Checkpointer is called when a required checkpoint triggers. Returning true
// means approved to continue; false means pause and persist state (HITL).
// Returning an error fails the run. For unattended tests the callback can
// auto-approve; for interactive CLI it prompts on stdin.
type Checkpointer func(cp Checkpoint, state *RunState) (bool, error)

// Runner orchestrates the manifest task loop with budgets and HITL pauses.
type Runner struct {
	Manifest     *Manifest
	Config       *config.Config
	Executor     Executor
	OnCheckpoint Checkpointer
	Clock        func() time.Time // nil = time.Now
	StateDir     string           // dir for .forge/runs/<run_id> persistence; "" = no persistence
	// SessionID is the daemon session backing Executor's turns (see
	// client.ManifestExecutor). Run persists it into RunState so a later
	// Resume can hand the same session back to the executor and keep the
	// conversational context tasks built up, instead of starting cold.
	SessionID string
	// WorkspaceRoot is the working directory for a task's mechanical
	// done_criteria command (see checkDoneCriteria). "" = ".".
	WorkspaceRoot string
	// Decompose, when true, calls Decomposer to populate Manifest.Tasks
	// before the task loop starts (only when the manifest declares no
	// explicit tasks — an already-authored task list is never overwritten).
	Decompose bool
	// Decomposer proposes a task breakdown from the manifest's goal+spec.
	// Required when Decompose is true; see run.Decomposer and
	// client.ManifestDecomposer for the daemon-backed implementation.
	Decomposer Decomposer

	budget BudgetState
	state  RunState
	// auditLog is non-nil only when sensitivity requires a tamper-evident
	// trail (RNF-4.10); persistState appends to it when set. Opened and
	// closed within execute, so it never outlives one Run/Resume call.
	auditLog *AuditLog

	// sensitivity reload cache: avoids re-reading config on every checkpoint
	// when the project file has not changed (cheap stat vs full parse).
	sensitivityLastMod  time.Time
	sensitivityLastSize int64
}

// budgetCtxKey is the context key for sharing the live BudgetState with
// cooperative executors so token/iteration limits can be checked mid-task.
type budgetCtxKey struct{}

// ContextWithBudget returns a child context carrying the live budget state.
// Cooperative executors may call BudgetFromContext and AddTurn/Check during
// execution to enforce token/iteration limits mid-task.
func ContextWithBudget(ctx context.Context, b *BudgetState) context.Context {
	return context.WithValue(ctx, budgetCtxKey{}, b)
}

// BudgetFromContext returns the budget stored via ContextWithBudget, if any.
func BudgetFromContext(ctx context.Context) *BudgetState {
	v := ctx.Value(budgetCtxKey{})
	if b, ok := v.(*BudgetState); ok {
		return b
	}
	return nil
}

// RunState is the reanudable audit log (RF-11.8).
type RunState struct {
	RunID            string      `json:"run_id"`
	Mode             string      `json:"mode"`
	Status           string      `json:"status"` // running | paused | completed | failed | killed
	StartedAt        time.Time   `json:"started_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
	SessionID        string      `json:"session_id,omitempty"`
	CompletedTasks   []string    `json:"completed_tasks"`
	CurrentTaskID    string      `json:"current_task_id,omitempty"`
	Budget           BudgetState `json:"budget"`
	PausedCheckpoint *Checkpoint `json:"paused_checkpoint,omitempty"`
	PauseReason      string      `json:"pause_reason,omitempty"`
	Error            string      `json:"error,omitempty"`
}

// Report is the final RF-11.10 report.
type Report struct {
	RunID             string      `json:"run_id"`
	Mode              string      `json:"mode"`
	Status            string      `json:"status"`
	SessionID         string      `json:"session_id,omitempty"`
	CompletedTasks    []string    `json:"completed_tasks"`
	TotalTasks        int         `json:"total_tasks"`
	PausedCheckpoints []string    `json:"paused_checkpoints"`
	Assumptions       []string    `json:"assumptions,omitempty"`
	Deviations        []string    `json:"deviations,omitempty"`
	BudgetUsed        BudgetState `json:"budget_used"`
	ValidationState   string      `json:"validation_state"` // all_tasks_passed | partial | failed
}

const (
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusKilled    = "killed" // budget wall hit
)

func (r *Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// reloadSensitivity re-reads project sensitivity from config on disk so a
// mid-run tightening (e.g., editing .forge/config.json from general to
// datos-sensibles) is observed without restart. It is safe to call frequently:
// a stat cache avoids full I/O when the file is unchanged, and failures are
// fail-safe (keep current value). Runner is single-threaded during Run, so no
// locking is required; the only concurrent reader is the executor goroutine
// which never accesses Config.
func (r *Runner) reloadSensitivity() {
	if r.Config == nil {
		return
	}
	pp, err := config.ProjectConfigPath()
	if err != nil {
		return
	}
	fi, err := os.Stat(pp)
	if err != nil {
		// No project file — keep current sensitivity (defaults already in Config).
		return
	}
	mod := fi.ModTime()
	size := fi.Size()
	if !r.sensitivityLastMod.IsZero() && mod.Equal(r.sensitivityLastMod) && size == r.sensitivityLastSize {
		return
	}
	gp, _ := config.GlobalConfigPath()
	// Mirror buildApp layering: global then project. Missing files are skipped
	// by Load; invalid files are ignored mid-run to stay fail-safe.
	paths := []string{}
	if gp != "" {
		paths = append(paths, gp)
	}
	paths = append(paths, pp)
	fresh, err := config.Load(paths...)
	if err != nil {
		return
	}
	r.Config.Project.Sensitivity = fresh.Project.Sensitivity
	r.sensitivityLastMod = mod
	r.sensitivityLastSize = size
}

func (r *Runner) budgetContext(parent context.Context) (context.Context, context.CancelFunc) {
	if r.budget.MaxWallClock == 0 {
		// No wall-clock limit — return a cancellable context that is still
		// budget-aware via ContextWithBudget for token/iteration mid-task checks.
		c, cancel := context.WithCancel(parent)
		return ContextWithBudget(c, &r.budget), cancel
	}
	rem := r.budget.RemainingWallClock(r.now())
	if rem <= 0 {
		rem = time.Millisecond
	}
	c, cancel := context.WithTimeout(parent, rem)
	return ContextWithBudget(c, &r.budget), cancel
}

// callExecutor runs the executor with intra-task budget enforcement, then —
// on success — mechanically verifies task.DoneCriteria when it's a "cmd:"
// check (checkDoneCriteria). A failing check is reported the same way an
// executor error is: it counts against max_retries_per_task and eventually
// trips the same implicit-retries-exhausted HITL checkpoint, instead of
// silently trusting the model's own claim that the task succeeded.
func (r *Runner) callExecutor(ctx context.Context, task Task) (ExecResult, error) {
	res, err := r.runExecutor(ctx, task)
	if err != nil {
		return res, err
	}
	if verr := r.checkDoneCriteria(ctx, task); verr != nil {
		return res, verr
	}
	return res, nil
}

// runExecutor runs the executor with intra-task budget enforcement. The
// wall-clock budget is enforced via the executor context deadline so a
// runaway task is cancelled promptly, not at the next task boundary.
// Token/iteration budgets are checked on the turn loop after each AddTurn,
// and cooperative executors that use ContextWithBudget/BudgetFromContext
// can also enforce them mid-task via the shared BudgetState.
func (r *Runner) runExecutor(ctx context.Context, task Task) (ExecResult, error) {
	execCtx, cancel := r.budgetContext(ctx)
	defer cancel()

	type outcome struct {
		res ExecResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := r.Executor(execCtx, task)
		ch <- outcome{res, err}
	}()

	// Poll for budget threshold while the executor runs so a cooperative
	// executor that updates the shared budget via BudgetFromContext is
	// observed mid-task. For non-cooperative executors the deadline still
	// provides wall-clock enforcement.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ExecResult{}, ctx.Err()
		case <-execCtx.Done():
			select {
			case o := <-ch:
				if o.err == nil && execCtx.Err() != nil {
					o.err = fmt.Errorf("wall-clock budget timeout: %w", execCtx.Err())
				}
				return o.res, o.err
			default:
				return ExecResult{}, fmt.Errorf("wall-clock budget timeout: %w", execCtx.Err())
			}
		case o := <-ch:
			if execCtx.Err() == context.DeadlineExceeded && o.err == nil {
				o.err = fmt.Errorf("wall-clock budget timeout: %w", execCtx.Err())
			}
			return o.res, o.err
		case <-ticker.C:
			if err := r.budget.Check(r.now()); err != nil {
				cancel()
			}
		}
	}
}

// doneCriteriaCmdPrefix marks a Task.DoneCriteria as a mechanical check
// rather than descriptive text — see Task.DoneCriteria's doc comment.
const doneCriteriaCmdPrefix = "cmd:"

// doneCriteriaTimeout bounds how long a done_criteria command may run.
// Independent of the task's own wall-clock budget: a hung verification
// command must not silently eat the whole run's remaining budget.
const doneCriteriaTimeout = 2 * time.Minute

// checkDoneCriteria mechanically verifies task.DoneCriteria when it starts
// with "cmd:" (after trimming whitespace); any other value (including
// empty) is purely descriptive and always passes — this is opt-in, not a
// behavior change for every existing manifest. The command runs directly
// via exec (no shell): split on whitespace like shell_exec's command+args,
// so no pipes/redirects/quoting — keep it to a single invocation such as
// "cmd: go test ./internal/foo/...". Exit 0 is success; anything else
// (non-zero exit, timeout, command not found) is a task failure, returned
// the same way an executor error is so it counts against
// max_retries_per_task instead of silently trusting the model.
func (r *Runner) checkDoneCriteria(ctx context.Context, task Task) error {
	trimmed := strings.TrimSpace(task.DoneCriteria)
	cmdLine, ok := strings.CutPrefix(trimmed, doneCriteriaCmdPrefix)
	if !ok {
		return nil
	}
	fields := strings.Fields(cmdLine)
	if len(fields) == 0 {
		return fmt.Errorf("task %s: done_criteria %q has an empty command after the %q prefix", task.ID, task.DoneCriteria, doneCriteriaCmdPrefix)
	}
	// Defense in depth against the decomposition prompt's "no shell
	// operators" instruction being ignored (observed in practice with a
	// real decomposer call: a proposed "go build ./... && go test ./...").
	// The command runs directly via exec, not through a shell, so any of
	// these would either silently do nothing useful or get passed as a
	// literal (and confusing) argument to the program instead of behaving
	// as the model intended — fail clearly instead.
	for _, tok := range fields {
		if strings.ContainsAny(tok, "&|;<>`$") {
			return fmt.Errorf("task %s: done_criteria %q contains a shell operator (%q) — done_criteria runs as a single direct program invocation, not through a shell; use one plain command or split into multiple tasks", task.ID, task.DoneCriteria, tok)
		}
	}

	root := r.WorkspaceRoot
	if root == "" {
		root = "."
	}
	checkCtx, cancel := context.WithTimeout(ctx, doneCriteriaTimeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, fields[0], fields[1:]...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		const maxOutput = 2000
		outStr := string(out)
		if len(outStr) > maxOutput {
			outStr = outStr[:maxOutput] + "...[truncated]"
		}
		return fmt.Errorf("task %s: done_criteria check %q failed: %w\noutput:\n%s", task.ID, cmdLine, err, outStr)
	}
	return nil
}

// Run executes the manifest task loop from a cold start. It is the entrypoint
// that enforces all three pillars at once: RF-11 decomposition + checkpoints,
// RNF-8 hard kills, and RNF-9 ceiling (validated beforehand but re-checked
// for defense in depth). To continue a run interrupted by a crash, client
// disconnect, or HITL pause, use Resume instead (RF-11.8).
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	if err := r.validateForExecution(); err != nil {
		return nil, err
	}

	start := r.now()
	r.budget = NewBudgetState(r.Manifest, start)
	r.state = RunState{
		RunID:     r.Manifest.RunID,
		Mode:      r.Manifest.Mode,
		Status:    StatusRunning,
		StartedAt: start,
		UpdatedAt: start,
		SessionID: r.SessionID,
		Budget:    r.budget,
	}
	_ = r.persistState()

	// Decomposition happens before the dry_run short-circuit below so a dry
	// run previews the ACTUAL decomposed plan, not just EffectiveTasks()'s
	// single-task fallback.
	if err := r.decomposeIfNeeded(ctx); err != nil {
		return r.failReport(err)
	}

	// Dry run never writes: validate plan and return without executing.
	if r.Manifest.Mode == ModeDryRun {
		return r.dryRunReport(), nil
	}

	return r.execute(ctx, false)
}

// decomposeIfNeeded populates r.Manifest.Tasks from r.Decomposer when
// Decompose is set and the manifest declares no explicit tasks (an
// already-authored task list is never overwritten). No-op otherwise,
// including on Resume — a resumed run never calls this at all, since its
// task list (decomposed or not) already exists from the interrupted attempt.
// Token/time spent on the decomposition call itself is NOT charged against
// the manifest's budget (RNF-8): it is a small, one-off planning call
// outside the per-task accounting loop, not a task.
func (r *Runner) decomposeIfNeeded(ctx context.Context) error {
	if !r.Decompose || len(r.Manifest.Tasks) > 0 {
		return nil
	}
	if r.Decomposer == nil {
		return fmt.Errorf("manifest requests decomposition (--decompose) but no Decomposer is wired")
	}
	proposed, err := r.Decomposer(ctx, r.Manifest.Goal, r.Manifest.Spec)
	if err != nil {
		return fmt.Errorf("decompose tasks: %w", err)
	}
	if errs := ValidateTasks(proposed); len(errs) > 0 {
		return fmt.Errorf("decomposer proposed an invalid task list: %w", errors.Join(errs...))
	}
	r.Manifest.Tasks = proposed
	if r.StateDir != "" {
		_ = persistDecomposedTasks(r.StateDir, r.Manifest.RunID, proposed) // best-effort audit trail
	}
	return nil
}

// Resume continues a previously interrupted run (RF-11.8): it reloads the
// RunState persisted under StateDir for this manifest's run_id, skips every
// task already recorded in CompletedTasks, and carries on from there instead
// of reprocessing the whole manifest. It refuses to resume a run that
// already reached a terminal state (completed, failed, or killed by a hard
// budget wall) — those aren't "interrupted", they're finished, and silently
// re-running them would contradict RNF-8's hard-wall guarantee.
func (r *Runner) Resume(ctx context.Context) (*Report, error) {
	if err := r.validateForExecution(); err != nil {
		return nil, err
	}
	if r.Manifest.Mode == ModeDryRun {
		return nil, fmt.Errorf("cannot resume a dry_run — dry runs execute nothing and persist no state")
	}
	if r.StateDir == "" {
		return nil, fmt.Errorf("resume requires StateDir pointing at the interrupted run's persisted state")
	}

	prev, err := LoadState(r.StateDir, r.Manifest.RunID)
	if err != nil {
		return nil, fmt.Errorf("resume: load previous state for run %q: %w", r.Manifest.RunID, err)
	}
	switch prev.Status {
	case StatusCompleted:
		return nil, fmt.Errorf("run %q already completed — nothing to resume", prev.RunID)
	case StatusFailed:
		return nil, fmt.Errorf("run %q failed — resume is not supported for a failed run; fix the underlying issue and start a new run", prev.RunID)
	case StatusKilled:
		return nil, fmt.Errorf("run %q was killed by a hard budget wall (RNF-8) — resume is not supported; raise the budget and start a new run instead", prev.RunID)
	}
	// prev.Status is "running" (crash mid-task) or "paused" (HITL) — both
	// are legitimately interrupted, not finished, so both resume.

	r.state = *prev
	r.state.Status = StatusRunning
	r.state.PausedCheckpoint = nil
	r.state.PauseReason = ""
	r.state.Error = ""
	r.state.UpdatedAt = r.now()
	if r.SessionID != "" {
		r.state.SessionID = r.SessionID
	}
	r.budget = prev.Budget
	_ = r.persistState()

	return r.execute(ctx, true)
}

// validateForExecution runs the checks shared by Run and Resume before any
// task executes.
func (r *Runner) validateForExecution() error {
	if r.Manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if r.Config == nil {
		return fmt.Errorf("config is required")
	}
	// dry_run never calls Executor (Run returns dryRunReport() before ever
	// reaching execute()), so it's the one mode allowed to run without one —
	// the CLI's own dry_run path passes nil (no daemon connection needed).
	if r.Executor == nil && r.Manifest.Mode != ModeDryRun {
		return fmt.Errorf("executor is required")
	}
	if err := r.Manifest.Validate(); err != nil {
		return fmt.Errorf("manifest invalid: %w", err)
	}
	if err := r.Manifest.ValidateAgainstSensitivity(r.Config); err != nil {
		return fmt.Errorf("sensitivity ceiling: %w", err)
	}
	if r.Manifest.IsolationRequired() && (r.Manifest.Git.Isolation == "" || r.Manifest.Git.Isolation == "none") {
		return fmt.Errorf("mode %q requires git.isolation worktree or branch (RNF-8.1) — manifest declares %q", r.Manifest.Mode, r.Manifest.Git.Isolation)
	}
	return nil
}

// execute runs the task loop against the already-initialized r.state/r.budget
// (set up by Run for a cold start or by Resume from persisted state). When
// resuming, the post-decomposition checkpoint is skipped — it was already
// approved in the original run — and any task whose ID is already in
// r.state.CompletedTasks is skipped rather than reprocessed (RF-11.8).
func (r *Runner) execute(ctx context.Context, resuming bool) (*Report, error) {
	// RNF-4.10: a regulado/datos-sensibles project gets a tamper-evident
	// hash-chained audit trail alongside the plain state.json snapshot.
	// OpenAuditLog replays any existing chain, so a Resume continues it
	// rather than starting a fresh one.
	if r.StateDir != "" && requiresTamperEvidentAudit(r.Config.Project.Sensitivity) {
		al, err := OpenAuditLog(r.StateDir, r.Manifest.RunID)
		if err != nil {
			return r.failReport(fmt.Errorf("open tamper-evident audit log (required for sensitivity %q, RNF-4.10): %w", r.Config.Project.Sensitivity, err))
		}
		r.auditLog = al
		defer func() {
			_ = r.auditLog.Close()
			r.auditLog = nil
		}()
	}

	// Spec decomposition pause (RF-11 step 1 post-decomposition). Only on a
	// cold start: a resume already passed this gate once.
	if !resuming {
		if cp := r.findCheckpoint(TriggerAfterDecomposition); cp != nil && cp.Required {
			approved, err := r.handleCheckpoint(ctx, *cp, "after spec decomposition")
			if err != nil {
				return r.failReport(err)
			}
			if !approved {
				return r.pauseReport(*cp, "after_spec_decomposition")
			}
		}
	}

	tasks := r.Manifest.EffectiveTasks()
	alreadyDone := make(map[string]bool, len(r.state.CompletedTasks))
	for _, id := range r.state.CompletedTasks {
		alreadyDone[id] = true
	}
	var reportPaused []string

	for idx, task := range tasks {
		if alreadyDone[task.ID] {
			continue // RF-11.8: completed before the interruption — do not reprocess.
		}
		select {
		case <-ctx.Done():
			return r.failReport(fmt.Errorf("run cancelled: %w", ctx.Err()))
		default:
		}

		// Hard budget wall BEFORE starting the task (RNF-8.2).
		if err := r.budget.Check(r.now()); err != nil {
			r.state.Status = StatusKilled
			r.state.Error = err.Error()
			r.state.UpdatedAt = r.now()
			_ = r.persistState()
			return r.killedReport(err)
		}
		// Budget threshold HITL (soft pause before kill).
		if cpID := r.budget.CheckThreshold(r.now(), r.Manifest.HITL.Checkpoints); cpID != "" {
			// Find the triggering checkpoint; pause only if required.
			for _, cp := range r.Manifest.HITL.Checkpoints {
				if cp.ID == cpID && cp.Required {
					approved, err := r.handleCheckpoint(ctx, cp, "budget_threshold")
					if err != nil {
						return r.failReport(err)
					}
					if !approved {
						reportPaused = append(reportPaused, cp.ID)
						return r.pauseReport(cp, "budget_threshold")
					}
					reportPaused = append(reportPaused, cp.ID)
					break
				}
			}
		}

		// RNF-9 high sensitivity forces a checkpoint before every task.
		// Re-evaluate ceiling after reload so mid-run tightening is observed.
		r.reloadSensitivity()
		needsHighCheckpoint := isHighSensitivity(r.Config.Project.Sensitivity)
		var beforeCP *Checkpoint
		if needsHighCheckpoint {
			beforeCP = &Checkpoint{ID: "sensitivity-high-before-task", Trigger: TriggerBeforeTask, Required: true}
		} else {
			beforeCP = r.findCheckpoint(TriggerBeforeTask)
		}
		if beforeCP != nil && beforeCP.Required {
			// For high sensitivity we always pause regardless of manifest Match; for
			// normal triggers we check Match filtering only for before_editing (not applicable here).
			approved, err := r.handleCheckpoint(ctx, *beforeCP, fmt.Sprintf("before task %s (%d/%d)", task.ID, idx+1, len(tasks)))
			if err != nil {
				return r.failReport(err)
			}
			if !approved {
				r.state.CurrentTaskID = task.ID
				return r.pauseReport(*beforeCP, "before_task")
			}
			reportPaused = append(reportPaused, beforeCP.ID)
		}

		r.state.CurrentTaskID = task.ID
		r.state.UpdatedAt = r.now()
		_ = r.persistState()

		// Execute with bounded retries (RF-11.4 circuit breaker).
		maxRetries := r.Manifest.Budget.MaxRetriesPerTask
		var lastErr error
		var lastExecRes ExecResult
		succeeded := false
		for attempt := 0; attempt <= maxRetries; attempt++ {
			// Wall-clock may have elapsed during previous attempt; re-check before retry.
			if err := r.budget.Check(r.now()); err != nil {
				r.state.Status = StatusKilled
				r.state.Error = err.Error()
				_ = r.persistState()
				return r.killedReport(err)
			}
			// Intra-task budget enforcement: wall-clock via deadline-bound executor
			// context (cancellation) and token/iteration via turn-loop check after
			// each AddTurn so a runaway task is killed promptly, not at the next
			// task boundary.
			execRes, tErr := r.callExecutor(ctx, task)
			r.budget.AddTurn(execRes.Tokens, execRes.Iterations)
			r.state.Budget = r.budget
			r.state.UpdatedAt = r.now()
			_ = r.persistState()
			lastExecRes = execRes
			if tErr == nil {
				succeeded = true
				lastErr = nil
				break
			}
			lastErr = tErr
			// Budget may have been exhausted by this attempt.
			if err := r.budget.Check(r.now()); err != nil {
				r.state.Status = StatusKilled
				r.state.Error = err.Error()
				_ = r.persistState()
				return r.killedReport(err)
			}
			if attempt == maxRetries {
				break
			}
		}
		if !succeeded {
			// RF-11.5: retries exhausted is an implicit HITL checkpoint, not a silent skip.
			implicit := Checkpoint{ID: "implicit-retries-exhausted", Trigger: TriggerAfterTask, Required: true}
			approved, _ := r.handleCheckpoint(ctx, implicit, fmt.Sprintf("task %s retries exhausted: %v", task.ID, lastErr))
			if !approved {
				r.state.Error = fmt.Sprintf("task %s failed after %d retries: %v", task.ID, maxRetries, lastErr)
				_ = r.persistState()
				return r.pauseReport(implicit, r.state.Error)
			}
			// If implicitly approved (e.g., test auto-approve), mark failed and continue only if dry? No,
			// per spec we still pause; the above branch already handles. For non-paused path we treat as failed.
			r.state.Status = StatusFailed
			r.state.Error = fmt.Sprintf("task %s failed after %d retries: %v", task.ID, maxRetries, lastErr)
			_ = r.persistState()
			return r.failReport(fmt.Errorf("%s", r.state.Error))
		}

		r.state.CompletedTasks = append(r.state.CompletedTasks, task.ID)
		r.state.UpdatedAt = r.now()
		r.state.Budget = r.budget
		_ = r.persistState()

		// Dedicated budget check AFTER task completes — catch exact-boundary exceed that
		// the pre-task check would miss (T1 case). Hard kill, not a checkpoint.
		if err := r.budget.Check(r.now()); err != nil {
			r.state.Status = StatusKilled
			r.state.Error = err.Error()
			_ = r.persistState()
			return r.killedReport(err)
		}

		// RNF-9 high sensitivity forces checkpoint after every task that executed
		// a write-class tool. We inspect the ACTUAL tool calls observed during the
		// turn (fs_write, shell_exec, git, etc.) and only fall back to the
		// goal-text heuristic when tool records are unavailable (nil ToolCalls).
		// A turn that executed a write-class tool must trigger the pause
		// regardless of how the goal text was phrased.
		r.reloadSensitivity()
		// Re-evaluate needsHighCheckpoint after reload for after_task as well.
		needsHighCheckpoint = isHighSensitivity(r.Config.Project.Sensitivity)
		needsAfterHigh := false
		if needsHighCheckpoint {
			if lastExecRes.ToolCalls != nil {
				needsAfterHigh = taskExecutedWrite(lastExecRes.ToolCalls)
			} else {
				// Heuristic fallback when tool records are unavailable.
				needsAfterHigh = taskTouchesFSOrShell(task)
			}
		}
		var afterCP *Checkpoint
		if needsAfterHigh {
			afterCP = &Checkpoint{ID: "sensitivity-high-after-task", Trigger: TriggerAfterTask, Required: true}
		} else {
			afterCP = r.findCheckpoint(TriggerAfterTask)
		}
		if afterCP != nil && afterCP.Required {
			approved, err := r.handleCheckpoint(ctx, *afterCP, fmt.Sprintf("after task %s", task.ID))
			if err != nil {
				return r.failReport(err)
			}
			if !approved {
				return r.pauseReport(*afterCP, "after_task")
			}
			reportPaused = append(reportPaused, afterCP.ID)
		}

		// Commit per task is logical at this point (git ops would happen here when wired
		// to a real worktree). Dry_run already returned; otherwise we record the intent.
		if r.Manifest.Git.CommitPerTask && r.Manifest.Mode != ModeDryRun {
			// No-op for this slice beyond audit; real git commit is a follow-up via
			// worktree branch integration (phase-2 store.BranchSession already provides the isolation primitive).
		}
	}

	// Pre-merge HITL (RNF-9.2 for regulado and spec 7.1).
	if cp := r.findCheckpoint(TriggerBeforeMerge); cp != nil && cp.Required {
		approved, err := r.handleCheckpoint(ctx, *cp, "before merge to base_branch")
		if err != nil {
			return r.failReport(err)
		}
		if !approved {
			return r.pauseReport(*cp, "before_merge")
		}
		reportPaused = append(reportPaused, cp.ID)
	} else if cfgSensitivityRequiresPreMerge(r.Config.Project.Sensitivity) {
		// Defense in depth: regulated without explicit pre-merge should have been rejected at
		// ValidateAgainstSensitivity, but if we reach here via direct Runner construction, pause.
		cp := Checkpoint{ID: "required-pre-merge", Trigger: TriggerBeforeMerge, Required: true}
		approved, err := r.handleCheckpoint(ctx, cp, "before merge (sensitivity ceiling)")
		if err != nil {
			return r.failReport(err)
		}
		if !approved {
			return r.pauseReport(cp, "before_merge sensitivity")
		}
		reportPaused = append(reportPaused, cp.ID)
	}

	r.state.Status = StatusCompleted
	r.state.UpdatedAt = r.now()
	r.state.CurrentTaskID = ""
	_ = r.persistState()

	rep := &Report{
		RunID:             r.Manifest.RunID,
		Mode:              r.Manifest.Mode,
		Status:            StatusCompleted,
		SessionID:         r.state.SessionID,
		CompletedTasks:    append([]string(nil), r.state.CompletedTasks...),
		TotalTasks:        len(tasks),
		PausedCheckpoints: reportPaused,
		BudgetUsed:        r.budget,
		ValidationState:   "all_tasks_passed",
	}
	if r.StateDir != "" {
		_ = r.persistReport(rep)
	}
	return rep, nil
}

func (r *Runner) findCheckpoint(trigger string) *Checkpoint {
	for i := range r.Manifest.HITL.Checkpoints {
		if r.Manifest.HITL.Checkpoints[i].Trigger == trigger {
			cp := r.Manifest.HITL.Checkpoints[i]
			return &cp
		}
	}
	return nil
}

func (r *Runner) handleCheckpoint(ctx context.Context, cp Checkpoint, reason string) (bool, error) {
	// Re-read sensitivity before every checkpoint so a mid-run config change
	// is observed without restart. This is the per-checkpoint variant of
	// the cheaper per-task-boundary reload (both are applied; this covers
	// checkpoints that occur outside the task loop as well, e.g. after_spec_decomposition).
	r.reloadSensitivity()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	if r.OnCheckpoint == nil {
		// No handler: required checkpoints default to PAUSE (never auto-approve silently).
		// This makes autonomy REAL: without an approval callback, HITL actually bites.
		r.state.PausedCheckpoint = &cp
		r.state.PauseReason = reason
		r.state.Status = StatusPaused
		r.state.UpdatedAt = r.now()
		_ = r.persistState()
		return false, nil
	}
	approved, err := r.OnCheckpoint(cp, &r.state)
	if err != nil {
		return false, err
	}
	if !approved {
		r.state.PausedCheckpoint = &cp
		r.state.PauseReason = reason
		r.state.Status = StatusPaused
		r.state.UpdatedAt = r.now()
		_ = r.persistState()
	}
	return approved, nil
}

func (r *Runner) pauseReport(cp Checkpoint, reason string) (*Report, error) {
	r.state.Status = StatusPaused
	r.state.PausedCheckpoint = &cp
	r.state.PauseReason = reason
	r.state.UpdatedAt = r.now()
	_ = r.persistState()
	tasks := r.Manifest.EffectiveTasks()
	rep := &Report{
		RunID:             r.Manifest.RunID,
		Mode:              r.Manifest.Mode,
		Status:            StatusPaused,
		SessionID:         r.state.SessionID,
		CompletedTasks:    append([]string(nil), r.state.CompletedTasks...),
		TotalTasks:        len(tasks),
		PausedCheckpoints: []string{cp.ID},
		BudgetUsed:        r.budget,
		ValidationState:   "paused_for_hitl",
		Assumptions:       []string{reason},
	}
	if r.StateDir != "" {
		_ = r.persistReport(rep)
	}
	return rep, fmt.Errorf("paused at checkpoint %q (%s): %s — awaiting approval", cp.ID, cp.Trigger, reason)
}

func (r *Runner) failReport(err error) (*Report, error) {
	r.state.Status = StatusFailed
	r.state.Error = err.Error()
	r.state.UpdatedAt = r.now()
	_ = r.persistState()
	tasks := r.Manifest.EffectiveTasks()
	rep := &Report{
		RunID:           r.Manifest.RunID,
		Mode:            r.Manifest.Mode,
		Status:          StatusFailed,
		SessionID:       r.state.SessionID,
		CompletedTasks:  append([]string(nil), r.state.CompletedTasks...),
		TotalTasks:      len(tasks),
		BudgetUsed:      r.budget,
		ValidationState: "failed",
		Deviations:      []string{err.Error()},
	}
	if r.StateDir != "" {
		_ = r.persistReport(rep)
	}
	return rep, err
}

func (r *Runner) killedReport(err error) (*Report, error) {
	tasks := r.Manifest.EffectiveTasks()
	rep := &Report{
		RunID:           r.Manifest.RunID,
		Mode:            r.Manifest.Mode,
		Status:          StatusKilled,
		SessionID:       r.state.SessionID,
		CompletedTasks:  append([]string(nil), r.state.CompletedTasks...),
		TotalTasks:      len(tasks),
		BudgetUsed:      r.budget,
		ValidationState: "failed",
		Deviations:      []string{err.Error()},
	}
	if r.StateDir != "" {
		_ = r.persistReport(rep)
	}
	return rep, err
}

func (r *Runner) dryRunReport() *Report {
	tasks := r.Manifest.EffectiveTasks()
	return &Report{
		RunID:           r.Manifest.RunID,
		Mode:            ModeDryRun,
		Status:          StatusCompleted,
		CompletedTasks:  []string{},
		TotalTasks:      len(tasks),
		BudgetUsed:      BudgetState{},
		ValidationState: "dry_run_no_writes",
		Assumptions:     []string{"dry_run: no tasks executed, no writes performed"},
	}
}

func isHighSensitivity(s string) bool {
	n, ok := configNormalize(s)
	if !ok {
		return false
	}
	return n == config.SensitivitySensitive
}

func cfgSensitivityRequiresPreMerge(s string) bool {
	n, ok := configNormalize(s)
	if !ok {
		return false
	}
	return n == config.SensitivityRegulated
}

func configNormalize(s string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(s))
	switch trimmed {
	case "general", "low":
		return config.SensitivityGeneral, true
	case "regulado", "regulated", "medium":
		return config.SensitivityRegulated, true
	case "datos-sensibles", "datos_sensibles", "datos sensibles", "sensitive", "high", "restricted":
		return config.SensitivitySensitive, true
	default:
		return "", false
	}
}

func taskTouchesFSOrShell(t Task) bool {
	g := strings.ToLower(t.Goal)
	// Heuristic fallback ONLY when tool records are unavailable. Real
	// enforcement inspects the actual tool calls executed during the turn.
	return strings.Contains(g, "fs.") || strings.Contains(g, "write") || strings.Contains(g, "shell") || strings.Contains(g, "file") || strings.Contains(g, "migrate")
}

func isWriteTool(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "fs_write", "fs.write", "shell_exec", "shell.exec", "git":
		return true
	}
	// Treat any tool whose name contains "write" as write-class for forward
	// compatibility (e.g., future fs_write_file variants).
	if strings.Contains(n, "write") {
		return true
	}
	return false
}

func taskExecutedWrite(toolCalls []string) bool {
	for _, tc := range toolCalls {
		if isWriteTool(tc) {
			return true
		}
	}
	return false
}
