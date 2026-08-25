// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
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

// setupRegistry creates a test registry with a permissive policy and returns the workspace path.
func setupRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	tmpDir := t.TempDir()

	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{
			Read:  []string{"./**"},
			Write: []string{"./**"},
		},
		Shell: perms.ShellPermissions{
			Allow: []string{"cmd", "powershell", "echo", "printf", "sleep", "false", "git"},
		},
		Git: perms.GitPermissions{
			Allow: []string{"status", "add", "commit", "log", "diff", "branch", "switch", "stash", "restore", "show", "remote", "fetch"},
		},
	}

	engine, err := perms.New(policy, tmpDir, slog.Default())
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	registry := NewDefaultRegistry(engine, tmpDir, slog.Default())
	return registry, tmpDir
}

// TestRegistry_Get tests getting tools from registry.
func TestRegistry_Get(t *testing.T) {
	registry, _ := setupRegistry(t)

	tool, ok := registry.Get("fs.read")
	if !ok {
		t.Error("fs.read should be registered")
	}
	if tool.Name() != "fs.read" {
		t.Errorf("Expected fs.read, got %s", tool.Name())
	}

	_, ok = registry.Get("nonexistent")
	if ok {
		t.Error("nonexistent tool should not be found")
	}
}

// TestRegistry_List tests listing tools.
func TestRegistry_List(t *testing.T) {
	registry, _ := setupRegistry(t)

	tools := registry.List()
	if len(tools) != 5 {
		t.Errorf("Expected 5 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}

	expected := []string{"fs.read", "fs.write", "fs.list", "shell.exec", "git"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("Missing tool: %s", name)
		}
	}
}

// TestRegistry_Execute_UnknownTool tests unknown tool error.
func TestRegistry_Execute_UnknownTool(t *testing.T) {
	registry, _ := setupRegistry(t)

	result, err := registry.Execute(context.Background(), "unknown.tool", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	if result.Content != "ERROR: unknown tool unknown.tool" {
		t.Errorf("Expected error for unknown tool, got: %s", result.Content)
	}
}

// TestRegistry_Execute_SchemaValidation tests schema validation.
func TestRegistry_Execute_SchemaValidation(t *testing.T) {
	registry, ws := setupRegistry(t)

	// Missing required field
	result, err := registry.Execute(context.Background(), "fs.read", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ERROR: schema validation: path: required field is missing" {
		t.Errorf("Expected missing field error, got: %s", result.Content)
	}

	// Wrong type for path
	result, err = registry.Execute(context.Background(), "fs.read", map[string]any{"path": 123})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ERROR: schema validation: path: expected string, got int" {
		t.Errorf("Expected type error, got: %s", result.Content)
	}

	// Wrong type for offset
	result, err = registry.Execute(context.Background(), "fs.read", map[string]any{"path": "test.txt", "offset": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ERROR: schema validation: offset: expected number, got string" {
		t.Errorf("Expected type error for offset, got: %s", result.Content)
	}

	// Valid args should pass validation
	testFile := filepath.Join(ws, "test.txt")
	os.WriteFile(testFile, []byte("content"), 0o644)

	result, err = registry.Execute(context.Background(), "fs.read", map[string]any{"path": testFile})
	if err != nil {
		t.Fatal(err)
	}
	// Should succeed (permission allowed) and return fenced content
	if !strings.Contains(result.Content, "content") {
		t.Errorf("Expected fenced content containing 'content', got: %s", result.Content)
	}
}

// TestRegistry_Execute_PermissionDenied tests permission denial.
func TestRegistry_Execute_PermissionDenied(t *testing.T) {
	// Registry with restrictive policy (no shell allowed)
	tmpDir := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{
			Read:  []string{"./**"},
			Write: []string{"./**"},
		},
		Shell: perms.ShellPermissions{
			Allow: []string{}, // No shell commands allowed
		},
		Git: perms.GitPermissions{
			Allow: []string{"status"},
		},
	}
	engine, err := perms.New(policy, tmpDir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewDefaultRegistry(engine, tmpDir, slog.Default())

	result, err := registry.Execute(context.Background(), "shell.exec", map[string]any{
		"command": "echo",
		"args":    []string{"hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should return DENIED content, not an error
	if result.Content != "DENIED: default-deny:shell.exec" {
		t.Errorf("Expected DENIED, got: %s", result.Content)
	}

	// Metadata should indicate denial
	denied, _ := result.Metadata["denied"].(bool)
	if !denied {
		t.Error("Metadata should have denied=true")
	}
	rule, _ := result.Metadata["rule"].(string)
	if rule != "default-deny:shell.exec" {
		t.Errorf("Expected rule default-deny:shell.exec, got %s", rule)
	}
}

// TestRegistry_Execute_Allowed tests allowed tool execution.
func TestRegistry_Execute_Allowed(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0o644)

	result, err := registry.Execute(context.Background(), "fs.read", map[string]any{"path": testFile})
	if err != nil {
		t.Fatal(err)
	}

	// Should be fenced
	if !contains(result.Content, "<<TOOL_RESULT:fs.read>>") {
		t.Errorf("Result should be fenced: %s", result.Content)
	}
	if !contains(result.Content, "</TOOL_RESULT:fs.read>") {
		t.Errorf("Result should have closing fence: %s", result.Content)
	}
	if !contains(result.Content, "<CONTENT>") {
		t.Errorf("Result should have CONTENT tag: %s", result.Content)
	}

	// Content inside fence should be the file content
	if !contains(result.Content, "hello") {
		t.Errorf("Result should contain file content: %s", result.Content)
	}
}

// TestRegistry_Execute_Redaction tests that redaction is applied.
func TestRegistry_Execute_Redaction(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	// Create a file with a secret
	testFile := filepath.Join(tmpDir, "secret.txt")
	os.WriteFile(testFile, []byte("api_key=sk-1234567890abcdef"), 0o644)

	result, err := registry.Execute(context.Background(), "fs.read", map[string]any{"path": testFile})
	if err != nil {
		t.Fatal(err)
	}

	// Secret should be redacted inside the fence
	if contains(result.Content, "sk-1234567890abcdef") {
		t.Errorf("Secret should be redacted: %s", result.Content)
	}
	if !contains(result.Content, "[REDACTED]") {
		t.Errorf("Should contain [REDACTED]: %s", result.Content)
	}
}

// TestRegistry_Execute_FsWrite tests fs.write through registry.
func TestRegistry_Execute_FsWrite(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	testFile := filepath.Join(tmpDir, "output.txt")

	result, err := registry.Execute(context.Background(), "fs.write", map[string]any{
		"path":     testFile,
		"content":  "test content",
		"encoding": "utf8",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should be fenced
	if !contains(result.Content, "<<TOOL_RESULT:fs.write>>") {
		t.Errorf("Result should be fenced: %s", result.Content)
	}

	// File should be written
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "test content" {
		t.Errorf("File not written correctly: %s", string(content))
	}
}

// TestRegistry_Execute_FsList tests fs.list through registry.
func TestRegistry_Execute_FsList(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("2"), 0o644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755)

	result, err := registry.Execute(context.Background(), "fs.list", map[string]any{
		"path":      tmpDir,
		"recursive": false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !contains(result.Content, "<<TOOL_RESULT:fs.list>>") {
		t.Errorf("Result should be fenced: %s", result.Content)
	}

	// Should contain entries
	if !contains(result.Content, "file1.txt") || !contains(result.Content, "file2.txt") {
		t.Errorf("Result should list files: %s", result.Content)
	}
}

// TestRegistry_Execute_ShellExec tests shell.exec through registry.
func TestRegistry_Execute_ShellExec(t *testing.T) {
	registry, _ := setupRegistry(t)

	// Use cmd /c echo - output may include banner but should be fenced
	result, err := registry.Execute(context.Background(), "shell.exec", map[string]any{
		"command": "cmd",
		"args":    []string{"/c", "echo", "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !contains(result.Content, "<<TOOL_RESULT:shell.exec>>") {
		t.Errorf("Result should be fenced: %s", result.Content)
	}
	// Just verify it produced some output (may include banner)
	if result.Content == "" {
		t.Errorf("Result should contain output: %s", result.Content)
	}
}

// TestRegistry_Execute_Git tests git through registry.
func TestRegistry_Execute_Git(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = tmpDir
	cmd.Run()

	result, err := registry.Execute(context.Background(), "git", map[string]any{
		"subcommand": "status",
		"workdir":    tmpDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !contains(result.Content, "<<TOOL_RESULT:git>>") {
		t.Errorf("Result should be fenced: %s", result.Content)
	}
}

// TestRegistry_Concurrent tests concurrent Execute calls.
func TestRegistry_Concurrent(t *testing.T) {
	registry, tmpDir := setupRegistry(t)

	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("content"), 0o644)

	const workers = 10
	const iterations = 10
	errCh := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		go func() {
			for i := 0; i < iterations; i++ {
				_, err := registry.Execute(context.Background(), "fs.read", map[string]any{"path": testFile})
				if err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	for i := 0; i < workers*iterations; i++ {
		select {
		case err := <-errCh:
			t.Fatal(err)
		default:
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
