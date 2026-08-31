// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// TestShellExecTool_Basic tests basic shell_exec functionality.
func TestShellExecTool_Basic(t *testing.T) {
	tool := newShellExecTool(nil)

	// Use PowerShell to output a string
	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "Write-Output 'hello world'"},
	}

	result, err := tool.Execute(context.Background(), req)
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

	// Use PowerShell for string formatting
	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "'a-b'"},
	}

	result, err := tool.Execute(context.Background(), req)
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

	// Use ping to create a long-running command (10 pings ~ 9 seconds)
	req := perms.Request{
		Kind:       perms.KindShell,
		Command:    "cmd",
		Args:       []string{"/c", "ping", "-n", "10", "127.0.0.1"},
		TimeoutSec: 1,
	}

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

	// PowerShell exit 1 returns exit code 1
	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "exit 1"},
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, _ := result.Metadata["exit_code"].(int)
	if exitCode == 0 {
		t.Error("Expected non-zero exit code")
	}
}

// TestShellExecTool_Truncation tests output truncation at 50KB.
func TestShellExecTool_Truncation(t *testing.T) {
	tool := newShellExecTool(nil)

	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "echo small output"},
	}
	result, err := tool.Execute(context.Background(), req)
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

	// Generate ~100KB output using PowerShell
	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "1..5000 | ForEach-Object { 'XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX' }"},
	}
	result, err := tool.Execute(context.Background(), req)
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

	// Use PowerShell to read file
	req := perms.Request{
		Kind:    perms.KindShell,
		Command: "powershell",
		Args:    []string{"-NoProfile", "-Command", "Get-Content test.txt"},
		Workdir: tmpDir,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Content, "content") {
		t.Errorf("Unexpected output: %q", result.Content)
	}
}
