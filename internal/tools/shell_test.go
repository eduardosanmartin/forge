// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// script returns the command+args that run a one-liner on this platform:
// PowerShell on Windows, sh elsewhere. These tests used to be PowerShell-
// only and failed on every non-Windows runner (first CI run, 2026-10-03).
func script(windows, unix string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-Command", windows}
	}
	return "sh", []string{"-c", unix}
}

func shellReq(windows, unix string) perms.Request {
	cmd, args := script(windows, unix)
	return perms.Request{Kind: perms.KindShell, Command: cmd, Args: args}
}

// TestShellExecTool_Basic tests basic shell_exec functionality.
func TestShellExecTool_Basic(t *testing.T) {
	tool := newShellExecTool(nil)

	result, err := tool.Execute(context.Background(), shellReq("Write-Output 'hello world'", "echo 'hello world'"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Content, "hello world") {
		t.Errorf("Unexpected output: %q", result.Content)
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}
}

// TestShellExecTool_Args tests shell_exec with arguments.
func TestShellExecTool_Args(t *testing.T) {
	tool := newShellExecTool(nil)

	result, err := tool.Execute(context.Background(), shellReq("'a-b'", "echo a-b"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Content, "a-b") {
		t.Errorf("Unexpected output: %q", result.Content)
	}
}

// TestShellExecTool_Timeout tests shell_exec timeout.
func TestShellExecTool_Timeout(t *testing.T) {
	tool := newShellExecTool(nil)

	// A command that runs ~10 s, cut at 1 s.
	req := shellReq("Start-Sleep -Seconds 10", "sleep 10")
	req.TimeoutSec = 1

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	timeout, _ := result.Metadata["timeout"].(bool)
	if !timeout {
		t.Error("Expected timeout=true in metadata")
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	// When context times out, exitCode should be -1
	if exitCode != -1 {
		t.Errorf("Expected exit code -1 for timeout, got %d", exitCode)
	}
}

// TestShellExecTool_NonZeroExit tests non-zero exit code.
func TestShellExecTool_NonZeroExit(t *testing.T) {
	tool := newShellExecTool(nil)

	result, err := tool.Execute(context.Background(), shellReq("exit 1", "exit 1"))
	if err != nil {
		t.Fatal(err)
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	if exitCode == 0 {
		t.Error("Expected non-zero exit code")
	}
}

// TestShellExecTool_Truncation tests that small output is not truncated.
func TestShellExecTool_Truncation(t *testing.T) {
	tool := newShellExecTool(nil)

	result, err := tool.Execute(context.Background(), shellReq("echo small output", "echo small output"))
	if err != nil {
		t.Fatal(err)
	}

	truncated, _ := result.Metadata["truncated"].(bool)
	if truncated {
		t.Error("Small output should not be truncated")
	}
}

// TestShellExecTool_LargeOutput tests truncation with large output.
func TestShellExecTool_LargeOutput(t *testing.T) {
	tool := newShellExecTool(nil)

	// ~330 KB of output.
	line := strings.Repeat("X", 64)
	result, err := tool.Execute(context.Background(), shellReq(
		"1..5000 | ForEach-Object { '"+line+"' }",
		"i=0; while [ $i -lt 5000 ]; do echo "+line+"; i=$((i+1)); done"))
	if err != nil {
		t.Fatal(err)
	}

	truncated, _ := result.Metadata["truncated"].(bool)
	if !truncated {
		t.Error("Large output should be truncated")
	}

	if len(result.Content) > 50*1024 {
		t.Errorf("Output should be truncated to 50KB, got %d bytes", len(result.Content))
	}
}

// TestShellExecTool_Workdir tests shell_exec with workdir.
func TestShellExecTool_Workdir(t *testing.T) {
	tool := newShellExecTool(nil)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := shellReq("Get-Content test.txt", "cat test.txt")
	req.Workdir = tmpDir

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Content, "content") {
		t.Errorf("Unexpected output: %q", result.Content)
	}
}
