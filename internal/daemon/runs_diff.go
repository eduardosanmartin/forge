package daemon

import (
	"context"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/run"
)

// MethodRunTaskDiff returns the diff of one task's commit in a run
// (RF-7.3: diffs in the GUI). Only isolated runs with commit_per_task have
// per-task commits (RNF-8.4).
const MethodRunTaskDiff = "run.task_diff"

// maxTaskDiffBytes caps a diff sent to a client.
const maxTaskDiffBytes = 256 * 1024

// RunTaskDiffParams for run.task_diff.
type RunTaskDiffParams struct {
	RunID    string `json:"run_id"`
	TaskID   string `json:"task_id"`
	StateDir string `json:"state_dir,omitempty"`
}

// RunTaskDiffResult for run.task_diff.
type RunTaskDiffResult struct {
	RunID     string `json:"run_id"`
	TaskID    string `json:"task_id"`
	Commit    string `json:"commit"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
}

// TaskDiff returns `git show --stat --patch` of the commit recorded for
// taskID in runID, through the permission-gated git tool.
func (m *SessionManager) TaskDiff(ctx context.Context, runID, taskID, stateDir string) (RunTaskDiffResult, error) {
	commits := map[string]string{}
	if res, ok := m.GetRun(runID); ok && res.Report != nil && res.Report.TaskCommits != nil {
		commits = res.Report.TaskCommits
	} else {
		if stateDir == "" {
			stateDir = "."
		}
		st, err := run.LoadState(stateDir, runID)
		if err != nil {
			return RunTaskDiffResult{}, fmt.Errorf("run %q: %w", runID, ErrRunNotFound)
		}
		if st.TaskCommits != nil {
			commits = st.TaskCommits
		}
	}
	sha, ok := commits[taskID]
	if !ok || sha == "" {
		return RunTaskDiffResult{}, fmt.Errorf("task %q of run %q has no commit (only isolated runs with commit_per_task record one, and tasks that changed nothing have none)", taskID, runID)
	}
	sessionID := ""
	if res, ok := m.GetRun(runID); ok {
		sessionID = res.SessionID
	}
	git := m.gitRunner(sessionID)
	if git == nil {
		return RunTaskDiffResult{}, fmt.Errorf("no git runner available")
	}
	out, code, denied, err := git(ctx, "show", []string{"--stat", "--patch", "--format=commit %H%n%n    %s%n", sha})
	switch {
	case denied != "":
		return RunTaskDiffResult{}, fmt.Errorf("git show denied by permission policy (%s): allow \"show\" in permissions.git.allow", denied)
	case err != nil:
		return RunTaskDiffResult{}, err
	case code != 0:
		return RunTaskDiffResult{}, fmt.Errorf("git show %s failed (exit %d): %s", sha, code, out)
	}
	res := RunTaskDiffResult{RunID: runID, TaskID: taskID, Commit: sha, Diff: out}
	if len(res.Diff) > maxTaskDiffBytes {
		res.Diff = res.Diff[:maxTaskDiffBytes]
		res.Truncated = true
	}
	return res, nil
}
