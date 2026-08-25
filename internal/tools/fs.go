// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/eduardosanmartin/forge/internal/pathmatch"
	"github.com/eduardosanmartin/forge/internal/perms"
)

// fsReadTool implements the fs.read tool.
type fsReadTool struct{}

func newFsReadTool() *fsReadTool { return &fsReadTool{} }

func (t *fsReadTool) Name() string { return "fs.read" }
func (t *fsReadTool) Description() string {
	return "Read a file from the filesystem. Supports offset/limit for paging. Binary files are returned as base64."
}

func (t *fsReadTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read (relative to workspace or absolute)",
			},
			"offset": map[string]any{
				"type":        "number",
				"description": "Byte offset to start reading from (default 0)",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "Maximum bytes to read (default: entire file from offset)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *fsReadTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	path := req.Path
	offset := req.Offset
	limit := req.Limit

	// Resolve path relative to workspace if needed
	absPath := path
	if !filepath.IsAbs(path) {
		// The permission engine already validated the path, but we need the absolute path for reading
		// We'll just use the path as-is since os.ReadFile handles relative paths
		absPath = path
	}

	// Read the file
	data, err := os.ReadFile(absPath)
	if err != nil {
		return Result{}, err
	}

	// Apply offset
	if offset > 0 {
		if offset >= int64(len(data)) {
			data = []byte{}
		} else {
			data = data[offset:]
		}
	}

	// Apply limit (limit <= 0 means no limit)
	if limit > 0 && int64(len(data)) > limit {
		data = data[:limit]
	}

	// Check if content is valid UTF-8
	isBinary := !isValidUTF8(data)
	var content string
	metadata := make(map[string]any)
	metadata["size"] = len(data)
	metadata["offset"] = offset
	if limit >= 0 {
		metadata["limit"] = limit
	} else {
		metadata["limit"] = len(data)
	}

	if isBinary {
		content = base64.StdEncoding.EncodeToString(data)
		metadata["encoding"] = "base64"
	} else {
		content = string(data)
		metadata["encoding"] = "utf8"
	}

	return Result{Content: content, Metadata: metadata}, nil
}

// fsWriteTool implements the fs.write tool.
type fsWriteTool struct{}

func newFsWriteTool() *fsWriteTool { return &fsWriteTool{} }

func (t *fsWriteTool) Name() string { return "fs.write" }
func (t *fsWriteTool) Description() string {
	return "Write a file to the filesystem. Creates parent directories if requested. Atomic write via temp+rename."
}

func (t *fsWriteTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write (relative to workspace or absolute)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write",
			},
			"encoding": map[string]any{
				"type":        "string",
				"description": "Encoding of content: utf8 or base64 (default utf8)",
				"enum":        []string{"utf8", "base64"},
			},
			"create_dirs": map[string]any{
				"type":        "boolean",
				"description": "Create parent directories if they don't exist (default false)",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *fsWriteTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	path := req.Path
	content := req.Content
	encoding := req.Encoding
	createDirs := req.CreateDirs

	// Decode content if base64
	var data []byte
	var err error
	if encoding == "base64" {
		data, err = base64.StdEncoding.DecodeString(content)
		if err != nil {
			return Result{}, err
		}
	} else {
		data = []byte(content)
	}

	// Create parent directories if requested
	if createDirs {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
	}

	// Atomic write: write to temp file then rename
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return Result{}, err
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return Result{}, err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return Result{}, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return Result{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return Result{}, err
	}

	// Get absolute path for metadata
	absPath, _ := filepath.Abs(path)

	metadata := map[string]any{
		"written":  len(data),
		"path":     absPath,
		"encoding": encoding,
	}

	// Return JSON string as content per spec
	resultJSON, _ := json.Marshal(map[string]any{
		"written": len(data),
		"path":    absPath,
	})

	return Result{Content: string(resultJSON), Metadata: metadata}, nil
}

// fsListTool implements the fs.list tool.
type fsListTool struct{}

func newFsListTool() *fsListTool { return &fsListTool{} }

func (t *fsListTool) Name() string { return "fs.list" }
func (t *fsListTool) Description() string {
	return "List directory contents. Supports recursive listing and glob pattern filtering."
}

func (t *fsListTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the directory to list (relative to workspace or absolute)",
			},
			"recursive": map[string]any{
				"type":        "boolean",
				"description": "List recursively (default false)",
			},
			"pattern": map[string]any{
				"type":        "string",
				"description": "Doublestar glob pattern to filter entries (via pathmatch)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *fsListTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	path := req.Path
	recursive := req.Recursive
	pattern := req.Pattern

	absPath := path
	if !filepath.IsAbs(path) {
		absPath = path
	}

	entries, err := listDir(absPath, recursive, pattern)
	if err != nil {
		return Result{}, err
	}

	resultJSON, _ := json.Marshal(entries)

	metadata := map[string]any{
		"count":     len(entries),
		"recursive": recursive,
	}
	if pattern != "" {
		metadata["pattern"] = pattern
	}

	return Result{Content: string(resultJSON), Metadata: metadata}, nil
}

// listDir lists directory entries, optionally recursively and with pattern filtering.
func listDir(root string, recursive bool, pattern string) ([]map[string]any, error) {
	var entries []map[string]any

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from root
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			// Skip the root directory itself
			if info.IsDir() && recursive {
				return nil
			}
			relPath = ""
		}
		relPath = filepath.ToSlash(relPath)

		// Apply pattern filter if provided
		if pattern != "" {
			matched := pathmatch.Match(pattern, relPath)
			if !matched {
				// Don't skip directories - their children might match the pattern
				// (e.g., "**/*.txt" should match files in non-matching directories)
				return nil
			}
		}

		entries = append(entries, map[string]any{
			"name":     info.Name(),
			"path":     relPath,
			"is_dir":   info.IsDir(),
			"size":     info.Size(),
			"mod_time": info.ModTime().UnixMilli(),
		})

		if info.IsDir() && !recursive {
			return filepath.SkipDir
		}
		return nil
	}

	if recursive {
		err := filepath.Walk(root, walkFn)
		return entries, err
	}

	// Non-recursive: read directory directly
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range dirEntries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		relPath := entry.Name()
		if pattern != "" {
			matched := pathmatch.Match(pattern, relPath)
			if !matched {
				continue
			}
		}
		entries = append(entries, map[string]any{
			"name":     info.Name(),
			"path":     relPath,
			"is_dir":   info.IsDir(),
			"size":     info.Size(),
			"mod_time": info.ModTime().UnixMilli(),
		})
	}
	return entries, nil
}

// isValidUTF8 checks if a byte slice is valid UTF-8.
func isValidUTF8(data []byte) bool {
	// Simple check: try to decode as UTF-8
	// In Go, strings are UTF-8, so we can just check if converting to string
	// and back produces the same bytes (approximately)
	// A more efficient check: scan for invalid UTF-8 sequences
	for i := 0; i < len(data); {
		r, size := decodeRune(data[i:])
		if r == '\uFFFD' && size == 1 {
			// Replacement char from invalid sequence
			return false
		}
		i += size
	}
	return true
}

// decodeRune decodes a single UTF-8 rune, returns rune and size.
// Simplified version - in practice we'd use utf8.DecodeRune.
func decodeRune(p []byte) (rune, int) {
	// This is a minimal UTF-8 validation
	if len(p) == 0 {
		return '\uFFFD', 0
	}
	if p[0] < 0x80 {
		return rune(p[0]), 1
	}
	// Multi-byte sequences - simplified check
	if len(p) >= 2 && p[0] >= 0xC0 && p[0] < 0xE0 {
		if p[1]&0xC0 == 0x80 {
			return rune(p[0]&0x1F)<<6 | rune(p[1]&0x3F), 2
		}
	}
	if len(p) >= 3 && p[0] >= 0xE0 && p[0] < 0xF0 {
		if p[1]&0xC0 == 0x80 && p[2]&0xC0 == 0x80 {
			return rune(p[0]&0x0F)<<12 | rune(p[1]&0x3F)<<6 | rune(p[2]&0x3F), 3
		}
	}
	if len(p) >= 4 && p[0] >= 0xF0 && p[0] < 0xF8 {
		if p[1]&0xC0 == 0x80 && p[2]&0xC0 == 0x80 && p[3]&0xC0 == 0x80 {
			return rune(p[0]&0x07)<<18 | rune(p[1]&0x3F)<<12 | rune(p[2]&0x3F)<<6 | rune(p[3]&0x3F), 4
		}
	}
	return '\uFFFD', 1
}
