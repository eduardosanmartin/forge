package tools

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

func newWorktreeEngine(t *testing.T, ws string, gitAllow []string) *perms.Engine {
	t.Helper()
	policy := perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: gitAllow},
	}
	eng, err := perms.New(policy, ws, slog.Default())
	if err != nil {
		t.Fatalf("perms.New: %v", err)
	}
	return eng
}

func registryWithGitAllow(t *testing.T, ws string, allow []string) *Registry {
	t.Helper()
	eng := newWorktreeEngine(t, ws, allow)
	return NewDefaultRegistry(eng, ws, slog.Default())
}

func TestGitWorktreeAdd_AllowAndDeny(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initGitRepo(t, repoDir)
	commitFile(t, repoDir, "a.txt", "hello", "init")

	// Registry that ALLOWS worktree.
	allowedRegistry := registryWithGitAllow(t, ws, []string{"status", "worktree", "branch"})

	worktreePath := filepath.Join(repoDir, "wt-task-1")
	// Use relative path "wt-task-1" resolved against repoDir workdir.
	result, err := allowedRegistry.Execute(context.Background(), "git_worktree_add", map[string]any{
		"path":    worktreePath,
		"workdir": repoDir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Content, "<<TOOL_RESULT:git_worktree_add>>") {
		t.Fatalf("expected fenced result, got %s", result.Content)
	}
	// Verify worktree actually exists via list.
	listRes, _ := allowedRegistry.Execute(context.Background(), "git_worktree_list", map[string]any{"workdir": repoDir})
	if !strings.Contains(listRes.Content, "wt-task-1") {
		t.Errorf("worktree list should contain wt-task-1, got %s", listRes.Content)
	}
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Errorf("worktree dir should exist: %v", statErr)
	}

	// DENY: path outside repository must be blocked by tool validation (even though perms allow).
	outsidePath := filepath.Join(os.TempDir(), "forge-outside-wt-"+t.Name())
	result2, _ := allowedRegistry.Execute(context.Background(), "git_worktree_add", map[string]any{
		"path":    outsidePath,
		"workdir": repoDir,
	})
	if !strings.Contains(result2.Content, "ERROR") || !strings.Contains(result2.Content, "outside repository") {
		t.Errorf("outside path should be denied with ERROR outside repository, got %s", result2.Content)
	}

	// Path traversal via relative .. should also be denied.
	traversal := filepath.Join(repoDir, "..", "outside2")
	result3, _ := allowedRegistry.Execute(context.Background(), "git_worktree_add", map[string]any{
		"path":    traversal,
		"workdir": repoDir,
	})
	if !strings.Contains(result3.Content, "ERROR") {
		t.Errorf("traversal path should be denied, got %s", result3.Content)
	}

	// DENY via perms: registry without worktree allowed should return DENIED.
	deniedRegistry := registryWithGitAllow(t, ws, []string{"status"})
	deniedResult, _ := deniedRegistry.Execute(context.Background(), "git_worktree_add", map[string]any{
		"path":    filepath.Join(repoDir, "wt-denied"),
		"workdir": repoDir,
	})
	if !strings.Contains(deniedResult.Content, "DENIED") {
		t.Errorf("should be DENIED when worktree not in allowlist, got %s", deniedResult.Content)
	}

	// Cleanup: remove allowed worktree via tool.
	rmRes, _ := allowedRegistry.Execute(context.Background(), "git_worktree_remove", map[string]any{
		"path":    worktreePath,
		"workdir": repoDir,
	})
	if !strings.Contains(rmRes.Content, "<<TOOL_RESULT:git_worktree_remove>>") {
		t.Logf("remove result: %s", rmRes.Content)
	}
}

func TestGitWorktreeRemove_DenyOutsideAndForce(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	os.MkdirAll(repoDir, 0o755)
	initGitRepo(t, repoDir)
	commitFile(t, repoDir, "a.txt", "hello", "init")
	reg := registryWithGitAllow(t, ws, []string{"worktree"})

	// Create a worktree to have something to remove.
	wtPath := filepath.Join(repoDir, "wt-rm")
	_, _ = reg.Execute(context.Background(), "git_worktree_add", map[string]any{"path": wtPath, "workdir": repoDir})

	// Force flag must be denied.
	forceRes, _ := reg.Execute(context.Background(), "git_worktree_remove", map[string]any{
		"path":    wtPath,
		"workdir": repoDir,
		"force":   true,
	})
	if !strings.Contains(forceRes.Content, "ERROR") || !strings.Contains(forceRes.Content, "force") {
		t.Errorf("force removal should be denied, got %s", forceRes.Content)
	}

	// Outside path denied.
	outside := filepath.Join(os.TempDir(), "not-in-repo-rm")
	outsideRes, _ := reg.Execute(context.Background(), "git_worktree_remove", map[string]any{
		"path":    outside,
		"workdir": repoDir,
	})
	if !strings.Contains(outsideRes.Content, "ERROR") || !strings.Contains(outsideRes.Content, "outside repository") {
		t.Errorf("outside remove should be denied, got %s", outsideRes.Content)
	}

	// Valid remove should succeed.
	validRes, _ := reg.Execute(context.Background(), "git_worktree_remove", map[string]any{
		"path":    wtPath,
		"workdir": repoDir,
	})
	if !strings.Contains(validRes.Content, "<<TOOL_RESULT:git_worktree_remove>>") {
		t.Errorf("valid remove should succeed, got %s", validRes.Content)
	}
}

func TestGitWorktreeList_AllowDeny(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	os.MkdirAll(repoDir, 0o755)
	initGitRepo(t, repoDir)
	commitFile(t, repoDir, "a.txt", "hello", "init")

	allowed := registryWithGitAllow(t, ws, []string{"worktree"})
	res, _ := allowed.Execute(context.Background(), "git_worktree_list", map[string]any{"workdir": repoDir})
	if !strings.Contains(res.Content, "<<TOOL_RESULT:git_worktree_list>>") {
		t.Errorf("allowed list should be fenced, got %s", res.Content)
	}

	denied := registryWithGitAllow(t, ws, []string{"status"})
	deniedRes, _ := denied.Execute(context.Background(), "git_worktree_list", map[string]any{"workdir": repoDir})
	if !strings.Contains(deniedRes.Content, "DENIED") {
		t.Errorf("denied list should be DENIED, got %s", deniedRes.Content)
	}
}

func TestGitBranchTask_CreateAndValidation(t *testing.T) {
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	os.MkdirAll(repoDir, 0o755)
	initGitRepo(t, repoDir)
	commitFile(t, repoDir, "a.txt", "hello", "init")

	reg := registryWithGitAllow(t, ws, []string{"branch", "status"})

	// Success: clean HEAD.
	res, _ := reg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "42",
		"workdir": repoDir,
	})
	if !strings.Contains(res.Content, "<<TOOL_RESULT:git_branch_task>>") {
		t.Fatalf("expected success, got %s", res.Content)
	}
	// Verify branch exists.
	cmd := exec.Command("git", "branch", "--list", "forge/task-42")
	cmd.Dir = repoDir
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "forge/task-42") {
		t.Errorf("branch forge/task-42 should exist, got %s", string(out))
	}

	// Duplicate should error.
	dupRes, _ := reg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "42",
		"workdir": repoDir,
	})
	if !strings.Contains(dupRes.Content, "ERROR") || !strings.Contains(dupRes.Content, "already exists") {
		t.Errorf("duplicate branch should error, got %s", dupRes.Content)
	}

	// Dirty HEAD should be blocked.
	os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("dirty"), 0o644)
	dirtyRes, _ := reg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "99",
		"workdir": repoDir,
	})
	if !strings.Contains(dirtyRes.Content, "ERROR") || !strings.Contains(dirtyRes.Content, "not clean") {
		t.Errorf("dirty HEAD should be blocked, got %s", dirtyRes.Content)
	}
	// Restore clean for remaining tests.
	exec.Command("git", "checkout", "--", "a.txt").Run()
	// Ensure actually clean: git reset.
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	cmd.Run()

	// Invalid task_id: injection attempt with slash.
	invalidRes, _ := reg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "../evil",
		"workdir": repoDir,
	})
	if !strings.Contains(invalidRes.Content, "ERROR") {
		t.Errorf("invalid task_id should be rejected, got %s", invalidRes.Content)
	}
	invalidRes2, _ := reg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "bad; rm -rf",
		"workdir": repoDir,
	})
	if !strings.Contains(invalidRes2.Content, "ERROR") {
		t.Errorf("task_id with spaces/semicolon should be rejected, got %s", invalidRes2.Content)
	}

	// Perm denied when branch not allowed.
	deniedReg := registryWithGitAllow(t, ws, []string{"status"})
	deniedRes, _ := deniedReg.Execute(context.Background(), "git_branch_task", map[string]any{
		"task_id": "100",
		"workdir": repoDir,
	})
	if !strings.Contains(deniedRes.Content, "DENIED") {
		t.Errorf("should be DENIED when branch not allowed, got %s", deniedRes.Content)
	}
}

func TestValidateTaskID_BranchName(t *testing.T) {
	if err := validateTaskID("abc-123_def"); err != nil {
		t.Errorf("valid task_id rejected: %v", err)
	}
	if err := validateTaskID(""); err == nil {
		t.Error("empty task_id should be rejected")
	}
	if err := validateTaskID("a/b"); err == nil {
		t.Error("task_id with slash should be rejected")
	}
	if err := validateBranchName("forge/task-1"); err != nil {
		t.Errorf("valid branch rejected: %v", err)
	}
	if err := validateBranchName("bad..branch"); err == nil {
		t.Error("branch with .. should be rejected")
	}
	if err := validateBranchName("-bad"); err == nil {
		t.Error("branch starting with - should be rejected")
	}
}

func TestIsInsideRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	inside := filepath.Join(repo, "wt", "a")
	if !isInsideRepo(inside, repo) {
		t.Error("inside path should be inside")
	}
	if isInsideRepo(filepath.Join(repo, "..", "outside"), repo) {
		t.Error("outside path should not be inside")
	}
	if isInsideRepo(repo, repo) {
		// isInsideRepo itself returns true for repo==target (rel == .)
		// caller blocks it separately
	} else {
		t.Error("repo root itself is considered inside by helper")
	}
}
