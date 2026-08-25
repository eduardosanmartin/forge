// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	cmd.Run()
}

func commitFile(t *testing.T, dir, filename, content, message string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644)
	cmd := exec.Command("git", "add", filename)
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = dir
	cmd.Run()
}

// TestGitTool_Basic tests basic git functionality.
func TestGitTool_Basic(t *testing.T) {
	tool := newGitTool()

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	commitFile(t, tmpDir, "test.txt", "test", "initial")

	req := perms.Request{
		Kind:       perms.KindGit,
		Subcommand: "status",
		Workdir:    tmpDir,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content == "" {
		t.Error("Expected non-empty output")
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	subcmd, _ := result.Metadata["subcommand"].(string)
	if subcmd != "status" {
		t.Errorf("Expected subcommand status, got %s", subcmd)
	}
}

// TestGitTool_Subcommands tests various git subcommands.
func TestGitTool_Subcommands(t *testing.T) {
	tool := newGitTool()

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	commitFile(t, tmpDir, "test.txt", "test", "initial")

	// Commands that should produce output in a basic repo
	outputCommands := []string{"status", "log", "branch", "show"}
	for _, subcmd := range outputCommands {
		t.Run(subcmd, func(t *testing.T) {
			req := perms.Request{
				Kind:       perms.KindGit,
				Subcommand: subcmd,
				Workdir:    tmpDir,
			}
			result, err := tool.Execute(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Content == "" {
				t.Errorf("%s: expected non-empty output", subcmd)
			}
			exitCode, _ := result.Metadata["exit_code"].(int)
			if exitCode != 0 {
				t.Errorf("%s: expected exit code 0, got %d", subcmd, exitCode)
			}
		})
	}

	// Commands that may produce empty output in a basic repo (no remotes, no changes)
	// We only check that they execute successfully (exit code 0)
	emptyOKCommands := []string{"diff", "remote", "fetch"}
	for _, subcmd := range emptyOKCommands {
		t.Run(subcmd, func(t *testing.T) {
			req := perms.Request{
				Kind:       perms.KindGit,
				Subcommand: subcmd,
				Workdir:    tmpDir,
			}
			result, err := tool.Execute(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			exitCode, _ := result.Metadata["exit_code"].(int)
			if exitCode != 0 {
				t.Errorf("%s: expected exit code 0, got %d", subcmd, exitCode)
			}
		})
	}
}

// TestGitTool_Workdir tests git with workdir parameter.
func TestGitTool_Workdir(t *testing.T) {
	tool := newGitTool()

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	req := perms.Request{
		Kind:       perms.KindGit,
		Subcommand: "status",
		Workdir:    tmpDir,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content == "" {
		t.Error("Expected non-empty output")
	}
}

// TestGitTool_Args tests git with arguments.
func TestGitTool_Args(t *testing.T) {
	tool := newGitTool()

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	commitFile(t, tmpDir, "test.txt", "test", "initial")
	commitFile(t, tmpDir, "test2.txt", "test2", "second")

	req := perms.Request{
		Kind:       perms.KindGit,
		Subcommand: "log",
		GitArgs:    []string{"--oneline", "-2"},
		Workdir:    tmpDir,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content == "" {
		t.Error("Expected non-empty output")
	}
}

// TestGitTool_NonZeroExit tests git command that fails.
func TestGitTool_NonZeroExit(t *testing.T) {
	tool := newGitTool()

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Try to show a non-existent commit
	req := perms.Request{
		Kind:       perms.KindGit,
		Subcommand: "show",
		GitArgs:    []string{"nonexistent"},
		Workdir:    tmpDir,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	if exitCode == 0 {
		t.Error("Expected non-zero exit code for failed command")
	}
}
