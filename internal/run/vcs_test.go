package run

import (
	"context"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
)

// fakeGit is a scripted GitRunner: it records every call and answers the
// few read operations branch isolation needs.
type fakeGit struct {
	calls   []string
	branch  string // current branch
	dirty   string // git status --porcelain output
	deny    string // subcommand to deny
	commits int
}

func (f *fakeGit) run(_ context.Context, sub string, args []string) (string, int, string, error) {
	f.calls = append(f.calls, strings.TrimSpace(sub+" "+strings.Join(args, " ")))
	if sub == f.deny {
		return "", -1, "default-deny:git", nil
	}
	switch sub {
	case "status":
		return f.dirty, 0, "", nil
	case "branch":
		return f.branch + "\n", 0, "", nil
	case "switch":
		f.branch = args[len(args)-1]
		return "", 0, "", nil
	case "diff":
		return "changed.go\n", 0, "", nil
	case "commit":
		f.commits++
		return "", 0, "", nil
	case "log":
		return "sha" + string(rune('0'+f.commits)) + "\n", 0, "", nil
	}
	return "", 0, "", nil
}

// nopGit is the default GitRunner for runner tests that are not about git:
// a clean repo on "main" where every operation succeeds.
func nopGit(ctx context.Context, sub string, args []string) (string, int, string, error) {
	return (&fakeGit{branch: "main"}).run(ctx, sub, args)
}

func approveAll(cp Checkpoint, _ *RunState) (bool, error) { return true, nil }

// RNF-8.1/8.4 regression: the runner used to only VALIDATE git.isolation
// and treat commit_per_task as a no-op, so autonomous work landed directly
// on the checked-out branch with no per-task commits.
func TestRunnerBranchIsolationCommitsPerTaskAndMergesOnApproval(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Git.MergeToBase = "auto_if_all_hitl_passed"
	g := &fakeGit{branch: "main"}
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: approveAll}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"status --porcelain -- . :(exclude).forge",
		"branch --show-current",
		"switch -c run/test-run",
	}
	for i, w := range want {
		if g.calls[i] != w {
			t.Fatalf("call %d = %q, want %q (all: %v)", i, g.calls[i], w, g.calls)
		}
	}
	if g.commits != 2 || len(rep.TaskCommits) != 2 {
		t.Fatalf("commits = %d, report task commits = %v, want one per task", g.commits, rep.TaskCommits)
	}
	joined := strings.Join(g.calls, "\n")
	if !strings.Contains(joined, "switch main\nmerge --no-ff -m forge(test-run): merge run run/test-run run/test-run") {
		t.Fatalf("expected a --no-ff merge into main after approval; calls:\n%s", joined)
	}
	if rep.MergeCommit == "" || rep.WorkBranch != "run/test-run" || rep.BaseBranch != "main" {
		t.Fatalf("report git fields = %+v", rep)
	}
}

func TestRunnerBranchIsolationNeverMergesWithoutApprovedCheckpoint(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Git.MergeToBase = "auto_if_all_hitl_passed"
	m.HITL.Checkpoints = nil // no before_merge checkpoint at all
	g := &fakeGit{branch: "main"}
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: approveAll}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "merge") {
			t.Fatalf("merged without an approved before_merge checkpoint: %v", g.calls)
		}
	}
	if len(rep.GitNotes) == 0 || !strings.Contains(rep.GitNotes[0], "not merged") {
		t.Fatalf("report should say the work was left unmerged, got %v", rep.GitNotes)
	}
}

func TestRunnerBranchIsolationRefusesDirtyTree(t *testing.T) {
	g := &fakeGit{branch: "main", dirty: " M main.go\n"}
	r := &Runner{Manifest: testManifest(ModeCheckpoint), Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: approveAll}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected a dirty-tree refusal, got %v", err)
	}
	for _, c := range g.calls {
		if strings.HasPrefix(c, "switch") {
			t.Fatal("must not switch branches on a dirty tree")
		}
	}
}

func TestRunnerBranchIsolationPolicyDenialNamesSubcommand(t *testing.T) {
	g := &fakeGit{branch: "main", deny: "commit"}
	r := &Runner{Manifest: testManifest(ModeCheckpoint), Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: approveAll}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `allow "commit" in permissions.git.allow`) {
		t.Fatalf("expected a policy denial naming commit, got %v", err)
	}
}

func TestRunnerWorktreeIsolationRefusedHonestly(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Git.Isolation = "worktree"
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: nopGit}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected worktree to be refused (not silently run unisolated), got %v", err)
	}
}

func TestRunnerBranchIsolationWithoutGitRunnerFailsClosed(t *testing.T) {
	r := &Runner{Manifest: testManifest(ModeCheckpoint), Config: config.Defaults(), Executor: okExecutor(5, 1)}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "git runner") {
		t.Fatalf("expected a fail-closed refusal without a git runner, got %v", err)
	}
}

func TestRunnerResumeReturnsToWorkBranch(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(ModeCheckpoint)
	g := &fakeGit{branch: "main"}
	pauseFirst := func(cp Checkpoint, _ *RunState) (bool, error) { return false, nil }
	m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "after", Trigger: TriggerAfterTask, Required: true})
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: pauseFirst, StateDir: dir}
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("expected the run to pause after the first task")
	}
	g.branch = "main" // the human switched away meanwhile
	g.calls = nil
	r2 := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(5, 1), RunGit: g.run, OnCheckpoint: approveAll, StateDir: dir}
	if _, err := r2.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(g.calls) < 2 || g.calls[1] != "switch run/test-run" {
		t.Fatalf("resume must switch back to the work branch first; calls: %v", g.calls)
	}
}

// RF-11 extraordinary case: tool output that looks like prompt injection
// pauses the run for a human even though the task passed.
func TestRunnerPausesOnSuspiciousToolOutput(t *testing.T) {
	m := testManifest(ModeAutonomous)
	m.HITL.Checkpoints = nil
	exec := func(_ context.Context, _ Task) (ExecResult, error) {
		return ExecResult{Tokens: 1, Iterations: 1, Suspicious: []string{"fs_read: asks to ignore previous instructions"}}, nil
	}
	var asked []string
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: exec, RunGit: nopGit,
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { asked = append(asked, cp.ID); return false, nil }}
	rep, err := r.Run(context.Background())
	if err == nil || rep == nil || rep.Status != StatusPaused {
		t.Fatalf("expected a pause, got report=%+v err=%v", rep, err)
	}
	if len(asked) == 0 || asked[0] != "suspicious-content" {
		t.Fatalf("checkpoints asked = %v, want suspicious-content", asked)
	}
	if len(rep.CompletedTasks) != 0 {
		t.Fatalf("a task that read suspicious content must not be recorded complete before approval: %v", rep.CompletedTasks)
	}
}

// RF-11.8: the manifest is persisted so a restarted daemon can resume by ID.
func TestRunnerPersistsManifestAndStatesAreListable(t *testing.T) {
	dir := t.TempDir()
	m := testManifest(ModeCheckpoint)
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), RunGit: nopGit, StateDir: dir,
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return false, nil }}
	_, _ = r.Run(context.Background())
	got, err := LoadManifest(dir, m.RunID)
	if err != nil || got.RunID != m.RunID || len(got.Tasks) != len(m.Tasks) {
		t.Fatalf("LoadManifest = %+v, %v", got, err)
	}
	states, err := ListStates(dir)
	if err != nil || len(states) != 1 || states[0].RunID != m.RunID {
		t.Fatalf("ListStates = %+v, %v", states, err)
	}
}

// RNF-8.3 regression: a descriptive done_criteria used to pass unchecked.
func TestRunnerDescriptiveCriteriaAreVerified(t *testing.T) {
	m := testManifest(ModeCheckpoint)
	m.Budget.MaxRetriesPerTask = 0
	m.Tasks = []Task{{ID: "t1", Goal: "add a README section", DoneCriteria: "README has an Install section"}}
	verdicts := 0
	r := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), RunGit: nopGit,
		Verify: func(_ context.Context, task Task) (bool, string, error) {
			verdicts++
			return false, "README.md has no Install heading", nil
		},
		OnCheckpoint: func(cp Checkpoint, _ *RunState) (bool, error) { return cp.ID != "implicit-retries-exhausted", nil }}
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "paused") || verdicts != 1 {
		t.Fatalf("an unmet descriptive criterion must fail the task like a failed check (verdicts=%d, err=%v)", verdicts, err)
	}

	r2 := &Runner{Manifest: m, Config: config.Defaults(), Executor: okExecutor(1, 1), RunGit: nopGit, OnCheckpoint: approveAll,
		Verify: func(_ context.Context, task Task) (bool, string, error) { return true, "found ## Install", nil }}
	rep, err := r2.Run(context.Background())
	if err != nil || rep.ValidationState != "all_tasks_passed" || len(rep.UnverifiedTasks) != 0 {
		t.Fatalf("verified run: rep=%+v err=%v", rep, err)
	}
}

func TestParseVerdict(t *testing.T) {
	cases := map[string]bool{
		`{"met": true, "evidence": "tests pass"}`:                    true,
		"Sure.\n```json\n{\"met\": false, \"evidence\": \"x\"}\n```": false,
		`I checked it. {"met": true, "evidence": "ok"} Done.`:        true,
	}
	for raw, want := range cases {
		v, err := ParseVerdict(raw)
		if err != nil || v.Met != want {
			t.Errorf("ParseVerdict(%q) = %+v, %v; want met=%v", raw, v, err, want)
		}
	}
	for _, bad := range []string{"looks good to me", `{"evidence": "no met field"}`, ""} {
		if _, err := ParseVerdict(bad); err == nil {
			t.Errorf("ParseVerdict(%q) must fail: an unreadable verdict never counts as met", bad)
		}
	}
}
