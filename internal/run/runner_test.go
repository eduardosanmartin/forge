package run

import (
	"context"
	"errors"
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
	return func(_ context.Context, _ Task) (int, int, error) {
		return tokens, iters, nil
	}
}

func failingExecutor(err error) Executor {
	return func(_ context.Context, _ Task) (int, int, error) {
		return 0, 1, err
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

func TestRunnerBudgetKillWallClock(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxWallClock = "1ms"
	cfg := config.Defaults()
	start := time.Now()
	calls := 0
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(10, 1),
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
		Executor: func(_ context.Context, _ Task) (int, int, error) {
			count++
			return 4, 1, nil
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
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(5, 1),
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

func TestRunnerDryRunNoExecution(t *testing.T) {
	m := testManifest(ModeDryRun)
	cfg := config.Defaults()
	called := false
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: func(_ context.Context, _ Task) (int, int, error) {
			called = true
			return 0, 0, nil
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

func TestRunnerRespectsContextCancel(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	cfg := config.Defaults()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{
		Manifest: m,
		Config:   cfg,
		Executor: okExecutor(5, 1),
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return true, nil },
	}
	_, err := r.Run(ctx)
	if err == nil {
		t.Fatal("expected cancel error")
	}
}
