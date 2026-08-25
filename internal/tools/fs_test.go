// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// TestFsReadTool_Basic tests basic fs.read functionality.
func TestFsReadTool_Basic(t *testing.T) {
	tool := newFsReadTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "Hello, World!\nThis is a test file.\n"
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatal(err)
	}

	req := perms.Request{
		Kind: perms.KindFsRead,
		Path: testFile,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Content != testContent {
		t.Errorf("Content mismatch:\ngot:  %q\nexpected: %q", result.Content, testContent)
	}

	if result.Metadata == nil {
		t.Error("Metadata should not be nil")
	}

	encoding, _ := result.Metadata["encoding"].(string)
	if encoding != "utf8" {
		t.Errorf("Expected encoding utf8, got %q", encoding)
	}
}

// TestFsReadTool_OffsetLimit tests offset and limit parameters.
func TestFsReadTool_OffsetLimit(t *testing.T) {
	tool := newFsReadTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "0123456789ABCDEF"
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Test offset
	req := perms.Request{
		Kind:   perms.KindFsRead,
		Path:   testFile,
		Offset: 5,
	}
	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	expected := "56789ABCDEF"
	if result.Content != expected {
		t.Errorf("Offset: got %q, expected %q", result.Content, expected)
	}

	// Test limit
	req = perms.Request{
		Kind:  perms.KindFsRead,
		Path:  testFile,
		Limit: 5,
	}
	result, err = tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	expected = "01234"
	if result.Content != expected {
		t.Errorf("Limit: got %q, expected %q", result.Content, expected)
	}

	// Test offset + limit
	req = perms.Request{
		Kind:   perms.KindFsRead,
		Path:   testFile,
		Offset: 5,
		Limit:  5,
	}
	result, err = tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	expected = "56789"
	if result.Content != expected {
		t.Errorf("Offset+Limit: got %q, expected %q", result.Content, expected)
	}
}

// TestFsReadTool_Binary tests binary file handling (base64 encoding).
func TestFsReadTool_Binary(t *testing.T) {
	tool := newFsReadTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "binary.bin")
	binaryData := []byte{0xFF, 0xFE, 0xFD, 0x00, 0x80, 0x81, 0x82}
	if err := os.WriteFile(testFile, binaryData, 0o644); err != nil {
		t.Fatal(err)
	}

	req := perms.Request{
		Kind: perms.KindFsRead,
		Path: testFile,
	}
	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	encoding, _ := result.Metadata["encoding"].(string)
	if encoding != "base64" {
		t.Errorf("Expected base64 encoding, got %q", encoding)
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Content)
	if err != nil {
		t.Fatalf("Failed to decode base64: %v", err)
	}
	if string(decoded) != string(binaryData) {
		t.Errorf("Decoded content mismatch")
	}
}

// TestFsReadTool_EmptyFile tests reading an empty file.
func TestFsReadTool_EmptyFile(t *testing.T) {
	tool := newFsReadTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(testFile, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	req := perms.Request{
		Kind: perms.KindFsRead,
		Path: testFile,
	}
	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content != "" {
		t.Errorf("Expected empty content, got %q", result.Content)
	}

	size, _ := result.Metadata["size"].(int)
	if size != 0 {
		t.Errorf("Expected size 0, got %d", size)
	}
}

// TestFsWriteTool_Basic tests basic fs.write functionality.
func TestFsWriteTool_Basic(t *testing.T) {
	tool := newFsWriteTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.txt")
	testContent := "Hello, World!"

	req := perms.Request{
		Kind:     perms.KindFsWrite,
		Path:     testFile,
		Content:  testContent,
		Encoding: "utf8",
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != testContent {
		t.Errorf("File content mismatch: got %q, expected %q", string(written), testContent)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(result.Content), &meta); err != nil {
		t.Fatalf("Result content not valid JSON: %v", err)
	}
	if meta["written"] != float64(len(testContent)) {
		t.Errorf("Written bytes mismatch: got %v, expected %d", meta["written"], len(testContent))
	}
}

// TestFsWriteTool_CreateDirs tests create_dirs option.
func TestFsWriteTool_CreateDirs(t *testing.T) {
	tool := newFsWriteTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "subdir", "nested", "output.txt")
	testContent := "Nested content"

	req := perms.Request{
		Kind:       perms.KindFsWrite,
		Path:       testFile,
		Content:    testContent,
		Encoding:   "utf8",
		CreateDirs: true,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != testContent {
		t.Errorf("File content mismatch")
	}

	_ = result
}

// TestFsWriteTool_Base64 tests base64 encoding.
func TestFsWriteTool_Base64(t *testing.T) {
	tool := newFsWriteTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "binary.bin")
	binaryData := []byte{0xFF, 0xFE, 0xFD, 0x00}
	encoded := base64.StdEncoding.EncodeToString(binaryData)

	req := perms.Request{
		Kind:     perms.KindFsWrite,
		Path:     testFile,
		Content:  encoded,
		Encoding: "base64",
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(binaryData) {
		t.Errorf("Binary content mismatch")
	}

	_ = result
}

// TestFsWriteTool_Overwrite tests overwriting existing file.
func TestFsWriteTool_Overwrite(t *testing.T) {
	tool := newFsWriteTool()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.txt")

	if err := os.WriteFile(testFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := perms.Request{
		Kind:     perms.KindFsWrite,
		Path:     testFile,
		Content:  "new",
		Encoding: "utf8",
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "new" {
		t.Errorf("File not overwritten: got %q", string(written))
	}

	_ = result
}

// TestFsListTool_Flat tests flat directory listing.
func TestFsListTool_Flat(t *testing.T) {
	tool := newFsListTool()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("2"), 0o644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755)

	req := perms.Request{
		Kind:      perms.KindFsRead,
		Path:      tmpDir,
		Recursive: false,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(result.Content), &entries); err != nil {
		t.Fatal(err)
	}

	if len(entries) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	for _, entry := range entries {
		if entry["name"] == nil || entry["path"] == nil || entry["is_dir"] == nil {
			t.Errorf("Entry missing required fields: %v", entry)
		}
	}
}

// TestFsListTool_Recursive tests recursive listing.
func TestFsListTool_Recursive(t *testing.T) {
	tool := newFsListTool()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "root.txt"), []byte("1"), 0o644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("2"), 0o644)
	os.Mkdir(filepath.Join(tmpDir, "subdir", "deep"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "deep", "deep.txt"), []byte("3"), 0o644)

	req := perms.Request{
		Kind:      perms.KindFsRead,
		Path:      tmpDir,
		Recursive: true,
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(result.Content), &entries); err != nil {
		t.Fatal(err)
	}

	if len(entries) != 5 {
		t.Errorf("Expected 5 entries, got %d: %v", len(entries), entries)
	}
}

// TestFsListTool_Pattern tests glob pattern filtering.
func TestFsListTool_Pattern(t *testing.T) {
	tool := newFsListTool()

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "file2.log"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "file3.txt"), []byte("3"), 0o644)
	os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("4"), 0o644)

	req := perms.Request{
		Kind:      perms.KindFsRead,
		Path:      tmpDir,
		Recursive: true,
		Pattern:   "**/*.txt",
	}

	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(result.Content), &entries); err != nil {
		t.Fatal(err)
	}

	txtCount := 0
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if filepath.Ext(name) == ".txt" {
			txtCount++
		} else {
			t.Errorf("Non-txt file matched: %s", name)
		}
	}
	if txtCount != 3 {
		t.Errorf("Expected 3 .txt files, got %d", txtCount)
	}
}
