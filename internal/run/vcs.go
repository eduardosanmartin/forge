package run

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// GitRunner runs one git subcommand through the permission-gated git tool
// (permissions.git.allow + the non-configurable git floor), exactly like a
// git call the agent itself proposes. denied is non-empty when the policy
// refused it; err reports an infrastructure failure; a non-zero exitCode is
// a git failure.
type GitRunner func(ctx context.Context, subcommand string, args []string) (output string, exitCode int, denied string, err error)

// gitIsolation implements RNF-8.1 (autonomous work never lands directly on
// the base branch) and RNF-8.4 (one atomic, revertible commit per task) for
// git.isolation "branch":
//
//   - begin: the workspace must be clean (outside .forge/); the run then
//     switches to its own work branch, created from the current HEAD.
//   - commitTask: after a task passes its done criteria, everything it
//     changed (outside .forge/) becomes one commit on the work branch.
//   - merge: only after an approved before_merge checkpoint, the work
//     branch is merged into the base branch with --no-ff (the whole run
//     stays revertible as one merge commit), and the workspace is left on
//     the base branch.
//
// Every step goes through GitRunner, so a policy that doesn't allow the
// needed subcommands (status, switch, add, commit, log, merge) fails the
// run with a message naming the subcommand — it never silently skips
// isolation.
type gitIsolation struct {
	run GitRunner
}

// forgeStatePathspec keeps forge's own runtime state (run state, audit
// logs, TUI state) out of the cleanliness check and out of task commits.
const forgeStatePathspec = ":(exclude).forge"

func (g gitIsolation) call(ctx context.Context, sub string, args ...string) (string, error) {
	out, code, denied, err := g.run(ctx, sub, args)
	switch {
	case denied != "":
		return "", fmt.Errorf("git %s denied by permission policy (%s) — branch isolation needs it: allow %q in permissions.git.allow", sub, denied, sub)
	case err != nil:
		return "", fmt.Errorf("git %s: %w", sub, err)
	case code != 0:
		return "", fmt.Errorf("git %s %s failed (exit %d): %s", sub, strings.Join(args, " "), code, tailForReport(strings.TrimSpace(out), 600))
	}
	return out, nil
}

// validBranchName is a conservative subset of git's ref rules.
var validBranchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// workBranchFor returns the manifest's work branch, or forge/run/<run_id>.
func workBranchFor(m *Manifest) (string, error) {
	b := strings.TrimSpace(m.Git.WorkBranch)
	if b == "" {
		b = "forge/run/" + m.RunID
	}
	if !validBranchName.MatchString(b) || strings.Contains(b, "..") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, "/") {
		return "", fmt.Errorf("invalid work branch name %q", b)
	}
	return b, nil
}

// begin prepares a cold-start run: clean tree, base branch recorded, work
// branch created and checked out.
func (g gitIsolation) begin(ctx context.Context, m *Manifest) (work, base string, err error) {
	if work, err = workBranchFor(m); err != nil {
		return "", "", err
	}
	status, err := g.call(ctx, "status", "--porcelain", "--", ".", forgeStatePathspec)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(status) != "" {
		return "", "", fmt.Errorf("the workspace has uncommitted changes — commit or stash them before an isolated run (RNF-8.1), so the run's commits contain only its own work:\n%s", tailForReport(strings.TrimSpace(status), 600))
	}
	current, err := g.call(ctx, "branch", "--show-current")
	if err != nil {
		return "", "", err
	}
	current = strings.TrimSpace(current)
	base = strings.TrimSpace(m.Git.BaseBranch)
	if base == "" {
		base = current
	}
	if base == "" {
		return "", "", fmt.Errorf("cannot determine the base branch (detached HEAD?) — set git.base_branch in the manifest")
	}
	if current != base {
		return "", "", fmt.Errorf("the workspace is on branch %q but the manifest's base_branch is %q — switch to the base branch first", current, base)
	}
	if _, err := g.call(ctx, "switch", "-c", work); err != nil {
		return "", "", err
	}
	return work, base, nil
}

// ensureOn switches back to the run's work branch when resuming.
func (g gitIsolation) ensureOn(ctx context.Context, work string) error {
	current, err := g.call(ctx, "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == work {
		return nil
	}
	_, err = g.call(ctx, "switch", work)
	return err
}

// commitTask commits everything the task changed. Returns "" when the task
// changed nothing (a verification-only task, for instance).
func (g gitIsolation) commitTask(ctx context.Context, runID string, task Task) (string, error) {
	if _, err := g.call(ctx, "add", "-A", "--", ".", forgeStatePathspec); err != nil {
		return "", err
	}
	staged, err := g.call(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(staged) == "" {
		return "", nil
	}
	subject := firstLine(task.Goal)
	if len(subject) > 72 {
		subject = subject[:72]
	}
	msg := fmt.Sprintf("forge(%s): %s %s\n\nTask %s of run %s, committed after its done criteria passed (RNF-8.4).", runID, task.ID, subject, task.ID, runID)
	if _, err := g.call(ctx, "commit", "-m", msg); err != nil {
		return "", err
	}
	return g.head(ctx)
}

// merge merges work into base with --no-ff and leaves the workspace on base.
func (g gitIsolation) merge(ctx context.Context, runID, work, base string) (string, error) {
	if _, err := g.call(ctx, "switch", base); err != nil {
		return "", err
	}
	if _, err := g.call(ctx, "merge", "--no-ff", "-m", fmt.Sprintf("forge(%s): merge run %s", runID, work), work); err != nil {
		return "", err
	}
	return g.head(ctx)
}

func (g gitIsolation) head(ctx context.Context) (string, error) {
	out, err := g.call(ctx, "log", "-1", "--format=%H")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
