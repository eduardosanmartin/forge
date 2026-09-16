package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
)

func testManifest(mode string) *Manifest {
	return &Manifest{
		RunID: "test-run",
		Mode:  mode,
		Goal:  "implement feature",
		Spec:  "SPEC stub",
		Budget: Budget{
			MaxWallClock:      "1h",
			MaxTokens:         1000,
			MaxIterations:     20,
			MaxRetriesPerTask: 2,
		},
		Git: GitConfig{Isolation: "branch", BaseBranch: "main", WorkBranch: "run/test-run", CommitPerTask: true, MergeToBase: "manual"},
		HITL: HITLConfig{Checkpoints: []Checkpoint{
			{ID: "pre-merge", Trigger: TriggerBeforeMerge, Required: true},
		}},
		Tasks: []Task{{ID: "t1", Goal: "task one"}, {ID: "t2", Goal: "task two"}},
	}
}

func okExecutor(tokens, iters int) Executor {
	return func(_ context.Context, _ Task) (ExecResult, error) {
		return ExecResult{Tokens: tokens, Iterations: iters}, nil
	}
}

func executorWithTools(tokens, iters int, tools []string) Executor {
	return func(_ context.Context, _ Task) (ExecResult, error) {
		// Return a non-nil slice to signal "records available".
		if tools == nil {
			tools = []string{}
		}
		return ExecResult{Tokens: tokens, Iterations: iters, ToolCalls: tools}, nil
	}
}

func failingExecutor(err error) Executor {
	return func(_ context.Context, _ Task) (ExecResult, error) {
		return ExecResult{Iterations: 1}, err
	}
}

func TestRunnerCompletesAllTasks(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(10, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			return true, nil
		},
		Clock: func() time.Time { return time.Now() },
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != StatusCompleted || len(rep.CompletedTasks) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if rep.ValidationState != "all_tasks_passed" {
		t.Fatalf("validation %q", rep.ValidationState)
	}
}

// TestRunnerOnProgressEmitsTaskLifecycle verifies OnProgress fires exactly
// the expected sequence of events for a run where every task succeeds first
// try: task_start then task_done for each task, in order, with correct
// 1-based TaskIndex/TotalTasks — the CLI progress bar (internal/cli/run.go)
// depends on this shape to render "task N/M" correctly.
func TestRunnerOnProgressEmitsTaskLifecycle(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	cfg := config.Defaults()
	var events []ProgressEvent
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(10, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		OnProgress:   func(ev ProgressEvent) { events = append(events, ev) },
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []struct {
		phase     ProgressPhase
		taskID    string
		taskIndex int
	}{
		{ProgressTaskStart, "t1", 1},
		{ProgressTaskDone, "t1", 1},
		{ProgressTaskStart, "t2", 2},
		{ProgressTaskDone, "t2", 2},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		ev := events[i]
		if ev.Phase != w.phase || ev.TaskID != w.taskID || ev.TaskIndex != w.taskIndex || ev.TotalTasks != 2 {
			t.Errorf("event[%d] = %+v, want phase=%s task=%s index=%d/2", i, ev, w.phase, w.taskID, w.taskIndex)
		}
	}
}

// TestRunnerOnProgressEmitsRetryAndFailed verifies a task that fails every
// attempt emits one task_retry per retry (carrying the failure that
// triggered it) followed by exactly one task_failed once retries are
// exhausted — not a task_done, and not silently nothing.
func TestRunnerOnProgressEmitsRetryAndFailed(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Tasks = []Task{{ID: "t1", Goal: "task one"}}
	m.Budget.MaxRetriesPerTask = 2
	cfg := config.Defaults()
	var events []ProgressEvent
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     failingExecutor(errors.New("boom")),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return false, nil },
		OnProgress:   func(ev ProgressEvent) { events = append(events, ev) },
	}
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("expected pause error after retries exhausted")
	}

	var phases []ProgressPhase
	for _, ev := range events {
		phases = append(phases, ev.Phase)
	}
	want := []ProgressPhase{ProgressTaskStart, ProgressTaskRetry, ProgressTaskRetry, ProgressTaskFailed}
	if len(phases) != len(want) {
		t.Fatalf("got phases %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Errorf("phase[%d] = %s, want %s", i, phases[i], want[i])
		}
	}
	last := events[len(events)-1]
	if last.Err == nil || last.Err.Error() != "boom" {
		t.Errorf("task_failed event should carry the failure, got %v", last.Err)
	}
}

func TestRunnerBudgetKillWallClock(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxWallClock = "1ms"
	cfg := config.Defaults()
	start := time.Now()
	calls := 0
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(10, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		Clock: func() time.Time {
			calls++
			if calls == 1 {
				return start
			}
			return start.Add(10 * time.Millisecond)
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "budget exceeded") {
		t.Fatalf("expected budget kill, got %v", err)
	}
	if r.state.Status != StatusKilled {
		t.Fatalf("state status %q want killed", r.state.Status)
	}
}

func TestRunnerBudgetKillTokens(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxTokens = 5
	cfg := config.Defaults()
	count := 0
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: func(_ context.Context, _ Task) (ExecResult, error) {
			count++
			return ExecResult{Tokens: 4, Iterations: 1}, nil
		},
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tokens") {
		t.Fatalf("expected token kill, got %v", err)
	}
	if count != 2 {
		t.Fatalf("expected kill after 2 tasks, count %d", count)
	}
}

func TestRunnerCheckpointPausesWhenNotApproved(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "before-t", Trigger: TriggerBeforeTask, Required: true})
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(10, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			if cp.Trigger == TriggerBeforeTask {
				return false, nil
			}
			return true, nil
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused at checkpoint") {
		t.Fatalf("expected pause, got %v", err)
	}
	if r.state.Status != StatusPaused {
		t.Fatalf("status %q want paused", r.state.Status)
	}
}

func TestRunnerNoCheckpointHandlerDefaultsToPause(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "before-t", Trigger: TriggerBeforeTask, Required: true})
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(10, 1),
		// OnCheckpoint nil -> must pause on required checkpoint
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected pause with nil handler, got %v", err)
	}
}

func TestRunnerHighSensitivityForcesCheckpointPerTask(t *testing.T) {
	m := testManifest(ModeSupervised) // supervised would normally not force per-task, but high sensitivity does
	// Remove explicit before_task so only sensitivity should trigger it
	m.HITL.Checkpoints = []Checkpoint{{ID: "pre-merge", Trigger: TriggerBeforeMerge, Required: true}}
	// Make tasks look like fs writes to trigger high sensitivity after_task as well
	m.Tasks = []Task{{ID: "t1", Goal: "write file foo"}, {ID: "t2", Goal: "write file bar"}}
	cfg := config.Defaults()
	cfg.Project.Sensitivity = config.SensitivitySensitive
	calls := 0
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			calls++
			return false, nil // pause at first HITL
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected high sensitivity pause, got %v", err)
	}
	if calls == 0 {
		t.Fatal("expected checkpoint callback invoked for high sensitivity")
	}
}

func TestRunnerRegulatedRequiresPreMerge(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.HITL.Checkpoints = nil // remove pre-merge
	cfg := config.Defaults()
	cfg.Project.Sensitivity = config.SensitivityRegulated
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sensitivity ceiling") {
		t.Fatalf("expected sensitivity ceiling rejection, got %v", err)
	}
}

func TestRunnerRetriesExhaustedPauses(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxRetriesPerTask = 1
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: failingExecutor(errors.New("compile error")),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			// auto-approve only non-implicit checkpoints
			if cp.ID == "implicit-retries-exhausted" {
				return false, nil
			}
			return true, nil
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected implicit pause after retries, got %v", err)
	}
}

// TestRunnerRetryFeedsBackPreviousFailure is a regression lock for a gap
// found running the wordstat example: a retry used to resend task.Goal
// completely unchanged, so ManifestExecutor (internal/client/run_manifest.go,
// which forwards only task.Goal to the model) gave the model no signal that
// its previous attempt failed a mechanical done_criteria check. Observed in
// practice: the model regenerated the IDENTICAL bug (same duplicate map key,
// same line) on every retry, burning the whole retry budget without ever
// converging. The fix appends the previous failure to the goal text on
// attempt > 0 so a retry is an informed correction, not a blind repeat.
func TestRunnerRetryFeedsBackPreviousFailure(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxRetriesPerTask = 1
	m.Tasks = []Task{{ID: "t1", Goal: "write internal/count/topwords.go"}}
	cfg := config.Defaults()

	var goalsSeen []string
	callCount := 0
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: func(_ context.Context, task Task) (ExecResult, error) {
			goalsSeen = append(goalsSeen, task.Goal)
			callCount++
			if callCount == 1 {
				return ExecResult{Iterations: 1}, errors.New(`done_criteria check "go build ./..." failed: duplicate key "no" in map literal`)
			}
			return ExecResult{Iterations: 1}, nil
		},
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(goalsSeen) != 2 {
		t.Fatalf("executor called %d times, want 2 (first attempt + one retry)", len(goalsSeen))
	}
	if goalsSeen[0] != "write internal/count/topwords.go" {
		t.Errorf("first attempt goal was rewritten: %q", goalsSeen[0])
	}
	if !strings.Contains(goalsSeen[1], "write internal/count/topwords.go") {
		t.Errorf("retry goal lost the original task text: %q", goalsSeen[1])
	}
	if !strings.Contains(goalsSeen[1], `duplicate key "no" in map literal`) {
		t.Errorf("retry goal does not mention the previous failure — retry is still blind: %q", goalsSeen[1])
	}
}

func TestRunnerDryRunNoExecution(t *testing.T) {
	m := testManifest(ModeDryRun)
	cfg := config.Defaults()
	called := false
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: func(_ context.Context, _ Task) (ExecResult, error) {
			called = true
			return ExecResult{}, nil
		},
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("dry_run: %v", err)
	}
	if called {
		t.Fatal("executor should not be called in dry_run")
	}
	if rep.ValidationState != "dry_run_no_writes" {
		t.Fatalf("dry_run validation %q", rep.ValidationState)
	}
}

func TestRunnerIsolationRequiredRejected(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Git.Isolation = "none"
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(1, 1),
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires git.isolation") {
		t.Fatalf("expected isolation required error, got %v", err)
	}
}

func TestStatePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(ModeCheckpoint)
	m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "before-t", Trigger: TriggerBeforeTask, Required: true})
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(10, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			return false, nil // pause
		},
		StateDir: dir,
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected pause, got %v", err)
	}
	loaded, err := LoadState(dir, m.RunID)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded.Status != StatusPaused || loaded.PausedCheckpoint == nil {
		t.Fatalf("loaded state %+v", loaded)
	}
	rep, err := LoadReport(dir, m.RunID)
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	if rep.Status != StatusPaused {
		t.Fatalf("report status %q", rep.Status)
	}
}

// simulateCrashAfterFirstTask persists a RunState as if a prior run had
// completed t1 and was interrupted (crash, disconnect) before t2 — status
// stays "running" because a real crash never gets to write a terminal
// status. It returns the StateDir so a fresh Runner can Resume from it.
func simulateCrashAfterFirstTask(t *testing.T, m *Manifest, sessionID string) string {
	t.Helper()
	dir := t.TempDir()
	prior := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: dir}
	start := time.Now().Add(-time.Minute)
	prior.budget = NewBudgetState(m, start)
	prior.budget.AddTurn(10, 1)
	prior.state = RunState{
		RunID:          m.RunID,
		Mode:           m.Mode,
		Status:         StatusRunning,
		StartedAt:      start,
		UpdatedAt:      start,
		SessionID:      sessionID,
		CompletedTasks: []string{"t1"},
		Budget:         prior.budget,
	}
	if err := prior.persistState(); err != nil {
		t.Fatalf("persist prior state: %v", err)
	}
	return dir
}

func TestResumeSkipsCompletedTasks(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	dir := simulateCrashAfterFirstTask(t, m, "sess-abc")

	var invoked []string
	r := &Runner{
		Manifest: m,
		Config:   config.Defaults(),
		Executor: func(_ context.Context, task Task) (ExecResult, error) {
			invoked = append(invoked, task.ID)
			return ExecResult{Tokens: 5, Iterations: 1}, nil
		},
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		StateDir:     dir,
	}
	rep, err := r.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(invoked) != 1 || invoked[0] != "t2" {
		t.Fatalf("expected executor invoked only for t2 (t1 already completed), got %v", invoked)
	}
	if rep.Status != StatusCompleted {
		t.Fatalf("report status %q, want completed", rep.Status)
	}
	if len(rep.CompletedTasks) != 2 || rep.CompletedTasks[0] != "t1" || rep.CompletedTasks[1] != "t2" {
		t.Fatalf("completed tasks %v, want [t1 t2]", rep.CompletedTasks)
	}
	if rep.SessionID != "sess-abc" {
		t.Fatalf("report session_id %q, want the session recorded before the crash", rep.SessionID)
	}

	loaded, err := LoadState(dir, m.RunID)
	if err != nil {
		t.Fatalf("LoadState after resume: %v", err)
	}
	if loaded.Status != StatusCompleted {
		t.Fatalf("persisted state status %q after resume completion", loaded.Status)
	}
}

func TestResumeRejectsTerminalStates(t *testing.T) {
	for _, status := range []string{StatusCompleted, StatusFailed, StatusKilled} {
		t.Run(status, func(t *testing.T) {
			m := testManifest(ModeCheckpoint)
			dir := t.TempDir()
			r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: dir}
			r.state = RunState{RunID: m.RunID, Mode: m.Mode, Status: status, StartedAt: time.Now(), UpdatedAt: time.Now()}
			if err := r.persistState(); err != nil {
				t.Fatalf("persist state: %v", err)
			}

			r2 := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: dir}
			_, err := r2.Resume(context.Background())
			if err == nil {
				t.Fatalf("expected Resume to reject a %s run, got nil error", status)
			}
		})
	}
}

func TestResumeRequiresStateDir(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1)}
	_, err := r.Resume(context.Background())
	if err == nil || !strings.Contains(err.Error(), "StateDir") {
		t.Fatalf("expected a StateDir-required error, got %v", err)
	}
}

func TestResumeWithoutPriorStateErrors(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: t.TempDir()}
	_, err := r.Resume(context.Background())
	if err == nil {
		t.Fatal("expected an error resuming a run with no persisted state")
	}
}

func TestResumeRejectsDryRun(t *testing.T) {
	m := testManifest(ModeDryRun)
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: t.TempDir()}
	_, err := r.Resume(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dry_run") {
		t.Fatalf("expected a dry_run rejection, got %v", err)
	}
}

// TestResumeDoesNotResetWallClockBudget guards against using a crash+resume
// cycle to dodge RNF-8's hard wall: the original StartedAt must survive the
// resume, so a run that was already past its wall-clock budget when it
// crashed gets killed immediately on resume instead of getting a fresh
// budget window.
func TestResumeDoesNotResetWallClockBudget(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxWallClock = "1m"
	dir := t.TempDir()
	prior := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: dir}
	longAgo := time.Now().Add(-time.Hour) // already far past the 1m budget
	prior.budget = NewBudgetState(m, longAgo)
	prior.state = RunState{
		RunID: m.RunID, Mode: m.Mode, Status: StatusRunning,
		StartedAt: longAgo, UpdatedAt: longAgo,
		CompletedTasks: []string{"t1"}, Budget: prior.budget,
	}
	if err := prior.persistState(); err != nil {
		t.Fatalf("persist prior state: %v", err)
	}

	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), StateDir: dir}
	rep, err := r.Resume(context.Background())
	if err == nil || !strings.Contains(err.Error(), "budget exceeded") {
		t.Fatalf("expected an immediate budget kill on resume, got report=%+v err=%v", rep, err)
	}
	if rep.Status != StatusKilled {
		t.Fatalf("report status %q, want killed", rep.Status)
	}
}

func TestRunnerRespectsContextCancel(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	cfg := config.Defaults()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	_, err := r.Run(ctx)
	if err == nil {
		t.Fatal("expected cancel error")
	}
}

func TestRunnerHighSensitivityToolCallInspection(t *testing.T) {
	cases := []struct {
		name       string
		goal       string
		toolCalls  []string
		wantPaused bool
	}{
		{
			name:       "adversarial innocuous goal but real fs_write must pause",
			goal:       "analyze codebase and summarize findings",
			toolCalls:  []string{"fs_read", "fs_write"},
			wantPaused: true,
		},
		{
			name:       "adversarial innocuous goal with shell_exec must pause",
			goal:       "review documentation for clarity",
			toolCalls:  []string{"shell_exec"},
			wantPaused: true,
		},
		{
			name:       "read-only tools must not trigger after_task pause",
			goal:       "analyze codebase and summarize findings",
			toolCalls:  []string{"fs_read", "fs_list"},
			wantPaused: false,
		},
		{
			name:       "empty tool set must not pause even if goal looks like write",
			goal:       "analyze codebase",
			toolCalls:  []string{},
			wantPaused: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testManifest(ModeSupervised)
			m.HITL.Checkpoints = []Checkpoint{{ID: "pre-merge", Trigger: TriggerBeforeMerge, Required: true}}
			m.Tasks = []Task{{ID: "t1", Goal: tc.goal}}
			cfg := config.Defaults()
			cfg.Project.Sensitivity = config.SensitivitySensitive
			paused := false
			r := &Runner{
				Manifest: m,
				Config:   cfg,
				Executor: executorWithTools(5, 1, tc.toolCalls),
				OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
					if cp.Trigger == TriggerAfterTask && cp.ID == "sensitivity-high-after-task" {
						paused = true
						return false, nil
					}
					// before_task high sensitivity also pauses; auto-approve it to reach after_task
					if cp.Trigger == TriggerBeforeTask {
						return true, nil
					}
					return true, nil
				},
			}
			_, err := r.Run(context.Background())
			isPaused := err != nil && strings.Contains(err.Error(), "paused")
			if tc.wantPaused && !isPaused {
				t.Fatalf("expected pause for tools %v, got err %v paused=%v", tc.toolCalls, err, paused)
			}
			if !tc.wantPaused && isPaused && paused {
				t.Fatalf("unexpected after_task pause for tools %v, err %v", tc.toolCalls, err)
			}
		})
	}
}

func TestRunnerHighSensitivityFallbackWhenToolRecordsUnavailable(t *testing.T) {
	t.Run("nil ToolCalls falls back to goal heuristic and pauses on write goal", func(t *testing.T) {
		m := testManifest(ModeSupervised)
		m.HITL.Checkpoints = []Checkpoint{{ID: "pre-merge", Trigger: TriggerBeforeMerge, Required: true}}
		m.Tasks = []Task{{ID: "t1", Goal: "write file foo"}}
		cfg := config.Defaults()
		cfg.Project.Sensitivity = config.SensitivitySensitive
		r := &Runner{
			Manifest: m,
			Config:   cfg,
			Executor: okExecutor(5, 1), // ToolCalls nil -> heuristic fallback
			OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
				if cp.Trigger == TriggerBeforeTask {
					return true, nil
				}
				if cp.Trigger == TriggerAfterTask {
					return false, nil
				}
				return true, nil
			},
		}
		_, err := r.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("expected pause via heuristic fallback, got %v", err)
		}
	})
	t.Run("nil ToolCalls with innocuous goal does not trigger after_task", func(t *testing.T) {
		m := testManifest(ModeSupervised)
		m.HITL.Checkpoints = []Checkpoint{{ID: "pre-merge", Trigger: TriggerBeforeMerge, Required: true}}
		m.Tasks = []Task{{ID: "t1", Goal: "analyze code"}}
		cfg := config.Defaults()
		cfg.Project.Sensitivity = config.SensitivitySensitive
		r := &Runner{
			Manifest: m,
			Config:   cfg,
			Executor: okExecutor(5, 1), // nil ToolCalls, innocuous goal
			OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
				// Approve before_task to reach after_task decision point.
				return true, nil
			},
		}
		rep, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("unexpected pause for innocuous goal with nil ToolCalls, err %v", err)
		}
		if rep.Status != StatusCompleted {
			t.Fatalf("status %q want completed", rep.Status)
		}
	})
}

func TestRunnerWritesVerifiableAuditLogUnderRegulatedSensitivity(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Project.Sensitivity = config.SensitivityRegulated
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		StateDir:     dir,
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != StatusCompleted {
		t.Fatalf("status %q want completed", rep.Status)
	}

	auditPath := filepath.Join(dir, ".forge", "runs", m.RunID, "audit.jsonl")
	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("expected an audit log at %s under regulado sensitivity: %v", auditPath, err)
	}
	res, err := VerifyAuditLog(auditPath)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.Valid || res.Records == 0 {
		t.Fatalf("expected a valid, non-empty chain, got %+v", res)
	}
}

func TestRunnerNoAuditLogUnderGeneralSensitivity(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	dir := t.TempDir()
	cfg := config.Defaults() // SensitivityGeneral by default
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		StateDir:     dir,
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	auditPath := filepath.Join(dir, ".forge", "runs", m.RunID, "audit.jsonl")
	if _, err := os.Stat(auditPath); !os.IsNotExist(err) {
		t.Fatalf("expected no audit log under general sensitivity, got err=%v", err)
	}
	// state.json should still exist — RF-11.8 resume support is unaffected.
	if _, err := os.Stat(filepath.Join(dir, ".forge", "runs", m.RunID, "state.json")); err != nil {
		t.Fatalf("expected state.json to still be written: %v", err)
	}
}

func TestRunnerAuditLogSurvivesResume(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Project.Sensitivity = config.SensitivityRegulated

	// Simulate a crash after t1, as in the resume tests above, but this time
	// starting from a real audited Run so the audit chain already has
	// content to resume.
	prior := &Runner{Manifest: m, Config: cfg, Executor: okExecutor(1, 1), StateDir: dir}
	start := time.Now().Add(-time.Minute)
	prior.budget = NewBudgetState(m, start)
	prior.state = RunState{
		RunID: m.RunID, Mode: m.Mode, Status: StatusRunning,
		StartedAt: start, UpdatedAt: start,
		CompletedTasks: []string{"t1"}, Budget: prior.budget,
	}
	al, err := OpenAuditLog(dir, m.RunID)
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	prior.auditLog = al
	if err := prior.persistState(); err != nil { // appends one audit record + writes state.json
		t.Fatalf("persistState: %v", err)
	}
	if err := al.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
		StateDir:     dir,
	}
	rep, err := r.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rep.Status != StatusCompleted {
		t.Fatalf("status %q want completed", rep.Status)
	}

	auditPath := filepath.Join(dir, ".forge", "runs", m.RunID, "audit.jsonl")
	res, err := VerifyAuditLog(auditPath)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected the chain to remain valid across the resume, got %+v", res)
	}
	if res.Records < 2 {
		t.Fatalf("expected records from both before and after the resume, got %d", res.Records)
	}
}

// --- decomposition (RF-11 task fragmentation) ---

func testManifestNoTasks(mode string) *Manifest {
	m := testManifest(mode)
	m.Tasks = nil
	return m
}

func fixedDecomposer(tasks []Task, err error) Decomposer {
	return func(_ context.Context, _, _ string) ([]Task, error) {
		return tasks, err
	}
}

func TestRunnerDecomposePopulatesTasksBeforeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	m := testManifestNoTasks(ModeCheckpoint)
	m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "post-decomp", Trigger: TriggerAfterDecomposition, Required: true})
	cfg := config.Defaults()
	var sawTasksAtCheckpoint int
	r := &Runner{
		Manifest:   m,
		Config:     cfg,
		Executor:   okExecutor(5, 1),
		Decompose:  true,
		Decomposer: fixedDecomposer([]Task{{ID: "d1", Goal: "decomposed task one"}, {ID: "d2", Goal: "decomposed task two"}}, nil),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			if cp.ID == "post-decomp" {
				sawTasksAtCheckpoint = len(m.Tasks)
			}
			return true, nil
		},
		StateDir: dir,
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sawTasksAtCheckpoint != 2 {
		t.Errorf("after_spec_decomposition checkpoint saw %d tasks, want 2 (decomposition must happen BEFORE this gate)", sawTasksAtCheckpoint)
	}
	if len(rep.CompletedTasks) != 2 || rep.CompletedTasks[0] != "d1" || rep.CompletedTasks[1] != "d2" {
		t.Errorf("completed tasks = %v, want [d1 d2] (decomposed tasks must actually run)", rep.CompletedTasks)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".forge", "runs", m.RunID, "tasks.decomposed.json"))
	if err != nil {
		t.Fatalf("expected tasks.decomposed.json to be persisted as an audit trail: %v", err)
	}
	if !strings.Contains(string(data), "decomposed task one") {
		t.Errorf("tasks.decomposed.json missing expected content: %s", data)
	}
}

func TestRunnerDecomposeSkippedWhenTasksAlreadyAuthored(t *testing.T) {
	m := testManifest(ModeCheckpoint) // already has hand-written Tasks
	cfg := config.Defaults()
	called := false
	r := &Runner{
		Manifest:  m,
		Config:    cfg,
		Executor:  okExecutor(5, 1),
		Decompose: true,
		Decomposer: func(_ context.Context, _, _ string) ([]Task, error) {
			called = true
			return nil, errors.New("should never be invoked")
		},
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called {
		t.Error("Decomposer must not run when the manifest already declares tasks")
	}
}

func TestRunnerDecomposeErrorFailsRun(t *testing.T) {
	m := testManifestNoTasks(ModeCheckpoint)
	cfg := config.Defaults()
	r := &Runner{
		Manifest:   m,
		Config:     cfg,
		Executor:   okExecutor(5, 1),
		Decompose:  true,
		Decomposer: fixedDecomposer(nil, errors.New("model unreachable")),
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "model unreachable") {
		t.Fatalf("expected decomposer error to fail the run, got %v", err)
	}
}

func TestRunnerDecomposeInvalidTaskListRejected(t *testing.T) {
	m := testManifestNoTasks(ModeCheckpoint)
	cfg := config.Defaults()
	r := &Runner{
		Manifest:   m,
		Config:     cfg,
		Executor:   okExecutor(5, 1),
		Decompose:  true,
		Decomposer: fixedDecomposer([]Task{{ID: "", Goal: "missing an id"}}, nil),
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid task list") {
		t.Fatalf("expected invalid decomposed tasks to fail the run, got %v", err)
	}
}

func TestRunnerDecomposeWithoutDecomposerErrors(t *testing.T) {
	m := testManifestNoTasks(ModeCheckpoint)
	cfg := config.Defaults()
	r := &Runner{Manifest: m, Config: cfg, Executor: okExecutor(5, 1), Decompose: true}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no Decomposer is wired") {
		t.Fatalf("expected a clear error when Decompose is set without a Decomposer, got %v", err)
	}
}

func TestRunnerDryRunPreviewsDecomposedTaskCount(t *testing.T) {
	m := testManifestNoTasks(ModeDryRun)
	cfg := config.Defaults()
	r := &Runner{
		Manifest:   m,
		Config:     cfg,
		Decompose:  true,
		Decomposer: fixedDecomposer([]Task{{ID: "d1", Goal: "a"}, {ID: "d2", Goal: "b"}, {ID: "d3", Goal: "c"}}, nil),
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.TotalTasks != 3 {
		t.Errorf("dry_run TotalTasks = %d, want 3 (the decomposed plan, not the single-task fallback)", rep.TotalTasks)
	}
}

// --- mechanical done_criteria verification (5.3: don't trust the model's own claim of success) ---

func TestRunnerDoneCriteriaCmdSuccessCompletesTask(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Tasks = []Task{{ID: "t1", Goal: "task one", DoneCriteria: "cmd: go version"}}
	cfg := config.Defaults()
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", rep.Status)
	}
}

func TestRunnerDoneCriteriaCmdFailureExhaustsRetries(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxRetriesPerTask = 1
	m.Tasks = []Task{{ID: "t1", Goal: "task one", DoneCriteria: "cmd: go __not_a_real_subcommand__"}}
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(5, 1), // the agent turn itself "succeeds" — only the mechanical check fails
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			return cp.ID != "implicit-retries-exhausted", nil
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("expected a failing done_criteria command to exhaust retries like any other task failure, got %v", err)
	}
}

func TestRunnerDoneCriteriaRejectsShellOperators(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxRetriesPerTask = 0
	m.Tasks = []Task{{ID: "t1", Goal: "task one", DoneCriteria: "cmd: go build ./... && go test ./..."}}
	cfg := config.Defaults()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) {
			return cp.ID != "implicit-retries-exhausted", nil
		},
	}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "shell operator") {
		t.Fatalf("expected a clear shell-operator rejection, got %v", err)
	}
}

func TestRunnerDoneCriteriaDescriptiveTextIsNeverChecked(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Tasks = []Task{{ID: "t1", Goal: "task one", DoneCriteria: "looks right to me, no command to run"}}
	cfg := config.Defaults()
	r := &Runner{
		Manifest:     m,
		Config:       cfg,
		Executor:     okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed (plain-text done_criteria must never be mechanically checked)", rep.Status)
	}
}
