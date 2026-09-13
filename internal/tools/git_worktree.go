// Package tools implements git worktree isolation and branch-per-task helpers for RF-10.1.
package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// gitWorktreeAddTool creates an isolated worktree inside the repository.
type gitWorktreeAddTool struct{}

func newGitWorktreeAddTool() *gitWorktreeAddTool { return &gitWorktreeAddTool{} }

func (t *gitWorktreeAddTool) Name() string { return "git_worktree_add" }
func (t *gitWorktreeAddTool) Description() string {
	return "Create a git worktree at a path inside the repository (deny-by-default, blocks paths outside the repo). Requires permissions.git.allow to include 'worktree'."
}
func (t *gitWorktreeAddTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Target directory for the new worktree. Must be inside the git repository. Relative paths are resolved against the repository root (or workdir if provided). Absolute paths outside the repo are denied.",
			},
			"branch": map[string]any{
				"type":        "string",
				"description": "Optional existing branch to checkout in the worktree. If omitted, HEAD is used. Branch names must be safe (no .., no leading -, no spaces).",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for git execution. OMIT to use the workspace root. Must be an existing directory.",
			},
		},
		"required": []string{"path"},
	}
}
func (t *gitWorktreeAddTool) PermsRequest(args map[string]any) (perms.Request, error) {
	workdir, _ := args["workdir"].(string)
	return perms.Request{Kind: perms.KindGit, Subcommand: "worktree", GitArgs: []string{"add"}, Workdir: workdir}, nil
}
func (t *gitWorktreeAddTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	args := req.Input
	if args == nil {
		args = map[string]any{}
	}
	pathArg, _ := args["path"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return Result{Content: "ERROR: path is required"}, nil
	}
	branch, _ := args["branch"].(string)
	workdir := req.Workdir
	if branch != "" {
		if err := validateBranchName(branch); err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}
	if workdir != "" {
		if _, err := validateWorkdir(workdir); err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}
	absPath, repoRoot, err := validateWorktreePath(ctx, workdir, pathArg)
	if err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}
	gitArgs := []string{}
	if workdir != "" {
		resolved, _ := validateWorkdir(workdir)
		if resolved != "" {
			gitArgs = append(gitArgs, "-C", resolved)
		} else {
			// workdir empty is already validated, but fallback: use it as-is
			gitArgs = append(gitArgs, "-C", workdir)
		}
	}
	// Build: git worktree add <absPath> [<branch>]
	gitArgs = append(gitArgs, "worktree", "add", absPath)
	if branch != "" {
		gitArgs = append(gitArgs, branch)
	}
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "git", gitArgs...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			exitCode = -2
		}
	}
	if exitCode != 0 {
		return Result{Content: string(out), Metadata: map[string]any{"exit_code": exitCode, "subcommand": "worktree", "repo_root": repoRoot, "path": absPath}}, nil
	}
	return Result{Content: string(out), Metadata: map[string]any{"exit_code": 0, "subcommand": "worktree", "repo_root": repoRoot, "path": absPath, "branch": branch}}, nil
}

// gitWorktreeListTool lists worktrees.
type gitWorktreeListTool struct{}

func newGitWorktreeListTool() *gitWorktreeListTool { return &gitWorktreeListTool{} }

func (t *gitWorktreeListTool) Name() string { return "git_worktree_list" }
func (t *gitWorktreeListTool) Description() string {
	return "List git worktrees (porcelain). Requires permissions.git.allow to include 'worktree'."
}
func (t *gitWorktreeListTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for git execution. OMIT to use the workspace root.",
			},
		},
		"required": []string{},
	}
}
func (t *gitWorktreeListTool) PermsRequest(args map[string]any) (perms.Request, error) {
	workdir, _ := args["workdir"].(string)
	return perms.Request{Kind: perms.KindGit, Subcommand: "worktree", GitArgs: []string{"list"}, Workdir: workdir}, nil
}
func (t *gitWorktreeListTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	workdir := req.Workdir
	if workdir != "" {
		if _, err := validateWorkdir(workdir); err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}
	gitArgs := []string{}
	if workdir != "" {
		resolved, _ := validateWorkdir(workdir)
		if resolved != "" {
			gitArgs = append(gitArgs, "-C", resolved)
		}
	}
	gitArgs = append(gitArgs, "worktree", "list", "--porcelain")
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "git", gitArgs...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			exitCode = -2
		}
	}
	return Result{Content: string(out), Metadata: map[string]any{"exit_code": exitCode, "subcommand": "worktree"}}, nil
}

// gitWorktreeRemoveTool removes a worktree; force is denied.
type gitWorktreeRemoveTool struct{}

func newGitWorktreeRemoveTool() *gitWorktreeRemoveTool { return &gitWorktreeRemoveTool{} }

func (t *gitWorktreeRemoveTool) Name() string { return "git_worktree_remove" }
func (t *gitWorktreeRemoveTool) Description() string {
	return "Remove a git worktree at a path inside the repository (deny-by-default, blocks paths outside the repo and denies --force). Requires permissions.git.allow to include 'worktree'."
}
func (t *gitWorktreeRemoveTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path of the worktree to remove. Must be inside the repository.",
			},
			"force": map[string]any{
				"type":        "boolean",
				"description": "Force removal. DENIED — any true value is rejected by the safety floor.",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for git execution. OMIT to use the workspace root.",
			},
		},
		"required": []string{"path"},
	}
}
func (t *gitWorktreeRemoveTool) PermsRequest(args map[string]any) (perms.Request, error) {
	workdir, _ := args["workdir"].(string)
	return perms.Request{Kind: perms.KindGit, Subcommand: "worktree", GitArgs: []string{"remove"}, Workdir: workdir}, nil
}
func (t *gitWorktreeRemoveTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	args := req.Input
	if args == nil {
		args = map[string]any{}
	}
	pathArg, _ := args["path"].(string)
	if strings.TrimSpace(pathArg) == "" {
		return Result{Content: "ERROR: path is required"}, nil
	}
	if v, ok := args["force"]; ok {
		if b, ok := v.(bool); ok && b {
			return Result{Content: "ERROR: force removal is denied by the safety floor"}, nil
		}
	}
	workdir := req.Workdir
	if workdir != "" {
		if _, err := validateWorkdir(workdir); err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}
	absPath, repoRoot, err := validateWorktreePath(ctx, workdir, pathArg)
	if err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}
	gitArgs := []string{}
	if workdir != "" {
		resolved, _ := validateWorkdir(workdir)
		if resolved != "" {
			gitArgs = append(gitArgs, "-C", resolved)
		}
	}
	gitArgs = append(gitArgs, "worktree", "remove", absPath)
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "git", gitArgs...)
	out, err2 := cmd.CombinedOutput()
	exitCode := 0
	if err2 != nil {
		if ee, ok := err2.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			exitCode = -2
		}
	}
	if exitCode != 0 {
		return Result{Content: string(out), Metadata: map[string]any{"exit_code": exitCode, "subcommand": "worktree", "repo_root": repoRoot, "path": absPath}}, nil
	}
	return Result{Content: string(out), Metadata: map[string]any{"exit_code": 0, "subcommand": "worktree", "repo_root": repoRoot, "path": absPath}}, nil
}

// gitBranchTaskTool creates a branch forge/task-<id> from a clean HEAD.
type gitBranchTaskTool struct{}

func newGitBranchTaskTool() *gitBranchTaskTool { return &gitBranchTaskTool{} }

func (t *gitBranchTaskTool) Name() string { return "git_branch_task" }
func (t *gitBranchTaskTool) Description() string {
	return "Create a branch forge/task-<id> from HEAD. Requires a clean working tree (no uncommitted changes). Requires permissions.git.allow to include 'branch'."
}
func (t *gitBranchTaskTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "Task identifier used to build the branch name forge/task-<id>. Must match ^[A-Za-z0-9_-]+$ (1..64 chars, no slashes).",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for git execution. OMIT to use the workspace root.",
			},
			"base_branch": map[string]any{
				"type":        "string",
				"description": "Optional base branch or commit to branch from (default HEAD). Must be a safe branch name when provided.",
			},
		},
		"required": []string{"task_id"},
	}
}
func (t *gitBranchTaskTool) PermsRequest(args map[string]any) (perms.Request, error) {
	workdir, _ := args["workdir"].(string)
	taskID, _ := args["task_id"].(string)
	branch := "forge/task-" + taskID
	return perms.Request{Kind: perms.KindGit, Subcommand: "branch", GitArgs: []string{branch}, Workdir: workdir}, nil
}
func (t *gitBranchTaskTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	args := req.Input
	if args == nil {
		args = map[string]any{}
	}
	taskID, _ := args["task_id"].(string)
	if err := validateTaskID(taskID); err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}
	branchName := "forge/task-" + taskID
	baseBranch, _ := args["base_branch"].(string)
	if baseBranch != "" {
		if err := validateBranchName(baseBranch); err != nil {
			return Result{Content: "ERROR: invalid base_branch: " + err.Error()}, nil
		}
	} else {
		baseBranch = "HEAD"
	}
	workdir := req.Workdir
	if workdir != "" {
		if _, err := validateWorkdir(workdir); err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}
	// Check HEAD clean.
	clean, statusOut, err := isHeadClean(ctx, workdir)
	if err != nil {
		return Result{Content: "ERROR: failed to check HEAD cleanliness: " + err.Error()}, nil
	}
	if !clean {
		return Result{Content: "ERROR: HEAD is not clean — uncommitted changes present:\n" + statusOut}, nil
	}
	// Check branch does not already exist.
	exists, err := branchExists(ctx, workdir, branchName)
	if err != nil {
		return Result{Content: "ERROR: failed to check branch existence: " + err.Error()}, nil
	}
	if exists {
		return Result{Content: "ERROR: branch " + branchName + " already exists"}, nil
	}
	gitArgs := []string{}
	if workdir != "" {
		resolved, _ := validateWorkdir(workdir)
		if resolved != "" {
			gitArgs = append(gitArgs, "-C", resolved)
		}
	}
	gitArgs = append(gitArgs, "branch", branchName, baseBranch)
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, "git", gitArgs...)
	out, err2 := cmd.CombinedOutput()
	exitCode := 0
	if err2 != nil {
		if ee, ok := err2.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			exitCode = -2
		}
		return Result{Content: string(out), Metadata: map[string]any{"exit_code": exitCode, "branch": branchName, "subcommand": "branch"}}, nil
	}
	return Result{Content: string(out), Metadata: map[string]any{"exit_code": 0, "branch": branchName, "subcommand": "branch", "base": baseBranch}}, nil
}

// Helpers.

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validateTaskID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("task_id is required")
	}
	if !taskIDRe.MatchString(id) {
		return fmt.Errorf("task_id %q must match ^[A-Za-z0-9_-]{1,64}$", id)
	}
	return nil
}

var branchComponentRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateBranchName(branch string) error {
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("branch is required")
	}
	if len(branch) > 255 {
		return fmt.Errorf("branch name too long")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch name must not start with '-'")
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch name must not contain '..'")
	}
	if strings.Contains(branch, " ") || strings.Contains(branch, "\t") {
		return fmt.Errorf("branch name must not contain whitespace")
	}
	if strings.Contains(branch, ":") || strings.Contains(branch, "*") || strings.Contains(branch, "?") || strings.Contains(branch, "[") || strings.Contains(branch, "^") || strings.Contains(branch, "~") {
		return fmt.Errorf("branch name contains forbidden character")
	}
	// Validate each slash-separated component.
	parts := strings.Split(branch, "/")
	for _, p := range parts {
		if p == "" {
			return fmt.Errorf("branch name must not contain empty component or leading/trailing '/'")
		}
		if p == "." || p == ".." {
			return fmt.Errorf("branch name component must not be '.' or '..'")
		}
		if strings.HasPrefix(p, ".") {
			return fmt.Errorf("branch name component must not start with '.'")
		}
		if strings.HasSuffix(p, ".lock") {
			return fmt.Errorf("branch name component must not end with '.lock'")
		}
		if !branchComponentRe.MatchString(p) {
			return fmt.Errorf("branch name component %q contains invalid characters", p)
		}
	}
	return nil
}

func gitRepoRoot(ctx context.Context, workdir string) (string, error) {
	args := []string{}
	if workdir != "" {
		resolved, err := validateWorkdir(workdir)
		if err != nil {
			return "", err
		}
		if resolved != "" {
			args = append(args, "-C", resolved)
		} else {
			args = append(args, "-C", workdir)
		}
	}
	args = append(args, "rev-parse", "--show-toplevel")
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", args...)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("not a git repository: %s", msg)
	}
	root := strings.TrimSpace(out.String())
	if root == "" {
		return "", fmt.Errorf("failed to resolve git top-level")
	}
	// Normalize: clean and evaluate to absolute.
	clean := filepath.Clean(root)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func validateWorktreePath(ctx context.Context, workdir, target string) (string, string, error) {
	repoRoot, err := gitRepoRoot(ctx, workdir)
	if err != nil {
		return "", "", err
	}
	absTarget := target
	if !filepath.IsAbs(target) {
		// Resolve relative against workdir if given, otherwise repo root.
		base := repoRoot
		if workdir != "" {
			if resolved, err := validateWorkdir(workdir); err == nil && resolved != "" {
				base = resolved
			} else {
				base = workdir
			}
			// If workdir itself is outside repo, gitRepoRoot already validated repoRoot via -C, but base may be outside.
			// We still resolve relative against that base then check inside repo — outside bases will fail the inside check as desired.
		}
		absTarget = filepath.Join(base, target)
	}
	absTarget = filepath.Clean(absTarget)
	// Ensure absolute.
	if !filepath.IsAbs(absTarget) {
		absTarget, err = filepath.Abs(absTarget)
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve path %q: %v", target, err)
		}
		absTarget = filepath.Clean(absTarget)
	}
	if absTarget == repoRoot {
		return "", "", fmt.Errorf("worktree path must not be the repository root itself")
	}
	if !isInsideRepo(absTarget, repoRoot) {
		return "", "", fmt.Errorf("worktree path %q is outside repository %q", absTarget, repoRoot)
	}
	return absTarget, repoRoot, nil
}

func isInsideRepo(absTarget, repoRoot string) bool {
	rel, err := filepath.Rel(repoRoot, absTarget)
	if err != nil {
		return false
	}
	if rel == "." {
		return true // but caller blocks repoRoot itself; for subdirs it's inside
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// On Windows, Rel may return absolute if different volume; treat as outside.
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

func isHeadClean(ctx context.Context, workdir string) (bool, string, error) {
	args := []string{}
	if workdir != "" {
		resolved, err := validateWorkdir(workdir)
		if err != nil {
			return false, "", err
		}
		if resolved != "" {
			args = append(args, "-C", resolved)
		}
	}
	args = append(args, "status", "--porcelain")
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return false, "", fmt.Errorf("git status failed: %v (%s)", err, out.String())
	}
	s := strings.TrimSpace(out.String())
	if s == "" {
		return true, "", nil
	}
	return false, s, nil
}

func branchExists(ctx context.Context, workdir, branch string) (bool, error) {
	args := []string{}
	if workdir != "" {
		resolved, err := validateWorkdir(workdir)
		if err != nil {
			return false, err
		}
		if resolved != "" {
			args = append(args, "-C", resolved)
		}
	}
	// Use branch --list which always exits 0 and is portable; empty output means not found.
	// This avoids show-ref's 128 vs 1 divergence on Windows for missing hierarchical refs.
	args = append(args, "branch", "--list", branch)
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git branch --list failed: %v (%s)", err, out.String())
	}
	if strings.TrimSpace(out.String()) == "" {
		return false, nil
	}
	return true, nil
}
