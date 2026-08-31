// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// TestToolInterface verifies that all tools implement the Tool interface.
func TestToolInterface(t *testing.T) {
	tools := []Tool{
		newFsReadTool(),
		newFsWriteTool(),
		newFsListTool(),
		newShellExecTool(nil),
		newGitTool(),
	}

	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			if tool.Name() == "" {
				t.Error("Name() should not be empty")
			}

			if tool.Description() == "" {
				t.Error("Description() should not be empty")
			}

			schema := tool.JSONSchema()
			if schema == nil {
				t.Error("JSONSchema() should not be nil")
			}

			// Execute should not panic with minimal request
			req := perms.Request{Kind: perms.Kind(tool.Name())}
			_, err := tool.Execute(context.Background(), req)
			// We expect an error for incomplete requests, but not a panic
			_ = err
		})
	}
}

// TestTool_JSONSchemaStructure tests that JSON schemas have required structure.
func TestTool_JSONSchemaStructure(t *testing.T) {
	tools := []Tool{
		newFsReadTool(),
		newFsWriteTool(),
		newFsListTool(),
		newShellExecTool(nil),
		newGitTool(),
	}

	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			schema := tool.JSONSchema()

			// Should be an object type
			if schema["type"] != "object" {
				t.Errorf("%s: schema type should be object, got %v", tool.Name(), schema["type"])
			}

			// Should have properties
			props, ok := schema["properties"].(map[string]any)
			if !ok || props == nil {
				t.Errorf("%s: schema should have properties", tool.Name())
			}

			// Should have required array (handle both []string and []any)
			requiredRaw := schema["required"]
			var required []any
			switch v := requiredRaw.(type) {
			case []any:
				required = v
			case []string:
				required = make([]any, len(v))
				for i, s := range v {
					required[i] = s
				}
			default:
				required = nil
			}
			if required == nil {
				t.Errorf("%s: schema should have required array", tool.Name())
			}

			// Each required field should exist in properties
			for _, req := range required {
				field, ok := req.(string)
				if !ok {
					continue
				}
				if _, exists := props[field]; !exists {
					t.Errorf("%s: required field %q not in properties", tool.Name(), field)
				}
			}
		})
	}
}

// TestValidateArgs tests the schema validation function.
func TestValidateArgs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":   map[string]any{"type": "string"},
			"age":    map[string]any{"type": "number"},
			"active": map[string]any{"type": "boolean"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"theme": map[string]any{"type": "string"},
				},
			},
		},
		"required": []any{"name"},
	}

	tests := []struct {
		name     string
		args     map[string]any
		wantErr  bool
		errField string
	}{
		{
			name:    "valid",
			args:    map[string]any{"name": "test", "age": 25, "active": true, "tags": []any{"a", "b"}, "config": map[string]any{"theme": "dark"}},
			wantErr: false,
		},
		{
			name:     "missing required",
			args:     map[string]any{"age": 25},
			wantErr:  true,
			errField: "name",
		},
		{
			name:     "wrong type string",
			args:     map[string]any{"name": 123},
			wantErr:  true,
			errField: "name",
		},
		{
			name:     "wrong type number",
			args:     map[string]any{"name": "test", "age": "twenty"},
			wantErr:  true,
			errField: "age",
		},
		{
			name:     "wrong type boolean",
			args:     map[string]any{"name": "test", "active": "yes"},
			wantErr:  true,
			errField: "active",
		},
		{
			name:     "wrong type array",
			args:     map[string]any{"name": "test", "tags": "not-array"},
			wantErr:  true,
			errField: "tags",
		},
		{
			name:     "array item wrong type",
			args:     map[string]any{"name": "test", "tags": []any{"a", 123}},
			wantErr:  true,
			errField: "tags[1]",
		},
		{
			name:     "wrong type object",
			args:     map[string]any{"name": "test", "config": "not-object"},
			wantErr:  true,
			errField: "config",
		},
		{
			name:     "nested object wrong type",
			args:     map[string]any{"name": "test", "config": map[string]any{"theme": 123}},
			wantErr:  true,
			errField: "config.theme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArgs(schema, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if tc.errField != "" {
					if !contains(err.Error(), tc.errField) {
						t.Errorf("Error should mention field %q: %v", tc.errField, err)
					}
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestBuildPermsRequest tests the BuildPermsRequest function.
func TestBuildPermsRequest(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		args      map[string]any
		wantErr   bool
		checkFunc func(*testing.T, perms.Request)
	}{
		{
			name:     "fs.read basic",
			toolName: "fs.read",
			args:     map[string]any{"path": "/test/file.txt"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindFsRead {
					t.Error("Kind should be fs.read")
				}
				if req.Path != "/test/file.txt" {
					t.Errorf("Path mismatch: %s", req.Path)
				}
			},
		},
		{
			name:     "fs.read with offset limit",
			toolName: "fs.read",
			args:     map[string]any{"path": "/test/file.txt", "offset": 100.0, "limit": 500.0},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Offset != 100 {
					t.Errorf("Offset mismatch: %d", req.Offset)
				}
				if req.Limit != 500 {
					t.Errorf("Limit mismatch: %d", req.Limit)
				}
			},
		},
		{
			name:     "fs.read missing path",
			toolName: "fs.read",
			args:     map[string]any{},
			wantErr:  true,
		},
		{
			name:     "fs.write basic",
			toolName: "fs.write",
			args:     map[string]any{"path": "/test/file.txt", "content": "hello"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindFsWrite {
					t.Error("Kind should be fs.write")
				}
				if req.Content != "hello" {
					t.Errorf("Content mismatch: %s", req.Content)
				}
				if req.Encoding != "utf8" {
					t.Errorf("Default encoding should be utf8: %s", req.Encoding)
				}
			},
		},
		{
			name:     "fs.write base64",
			toolName: "fs.write",
			args:     map[string]any{"path": "/test/file.txt", "content": "SGVsbG8=", "encoding": "base64", "create_dirs": true},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Encoding != "base64" {
					t.Errorf("Encoding mismatch: %s", req.Encoding)
				}
				if !req.CreateDirs {
					t.Error("CreateDirs should be true")
				}
			},
		},
		{
			name:     "fs.list with pattern",
			toolName: "fs.list",
			args:     map[string]any{"path": "/test", "recursive": true, "pattern": "*.txt"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindFsRead {
					t.Error("Kind should be fs.read for list")
				}
				if !req.Recursive {
					t.Error("Recursive should be true")
				}
				if req.Pattern != "*.txt" {
					t.Errorf("Pattern mismatch: %s", req.Pattern)
				}
			},
		},
		{
			name:     "shell.exec basic",
			toolName: "shell.exec",
			args:     map[string]any{"command": "echo", "args": []any{"hello"}},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindShell {
					t.Error("Kind should be shell.exec")
				}
				if req.Command != "echo" {
					t.Errorf("Command mismatch: %s", req.Command)
				}
				if len(req.Args) != 1 || req.Args[0] != "hello" {
					t.Errorf("Args mismatch: %v", req.Args)
				}
			},
		},
		{
			name:     "shell.exec with timeout and workdir",
			toolName: "shell.exec",
			args:     map[string]any{"command": "sleep", "args": []any{"10"}, "timeout_sec": 30.0, "workdir": "/tmp"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.TimeoutSec != 30 {
					t.Errorf("TimeoutSec mismatch: %d", req.TimeoutSec)
				}
				if req.Workdir != "/tmp" {
					t.Errorf("Workdir mismatch: %s", req.Workdir)
				}
			},
		},
		{
			name:     "git basic",
			toolName: "git",
			args:     map[string]any{"subcommand": "status"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindGit {
					t.Error("Kind should be git")
				}
				if req.Subcommand != "status" {
					t.Errorf("Subcommand mismatch: %s", req.Subcommand)
				}
			},
		},
		{
			name:     "git with args and workdir",
			toolName: "git",
			args:     map[string]any{"subcommand": "log", "args": []any{"--oneline", "-5"}, "workdir": "/repo"},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if len(req.GitArgs) != 2 {
					t.Errorf("GitArgs mismatch: %v", req.GitArgs)
				}
				if req.Workdir != "/repo" {
					t.Errorf("Workdir mismatch: %s", req.Workdir)
				}
			},
		},
		{
			name:     "git missing subcommand",
			toolName: "git",
			args:     map[string]any{},
			wantErr:  true,
		},
		{
			name:     "custom v1 tool maps to KindCustom with tool name",
			toolName: "retrieval.search",
			args:     map[string]any{"query": "hello", "k": 5.0},
			wantErr:  false,
			checkFunc: func(t *testing.T, req perms.Request) {
				if req.Kind != perms.KindCustom {
					t.Errorf("Kind = %q, want %q", req.Kind, perms.KindCustom)
				}
				if req.Command != "retrieval.search" {
					t.Errorf("Command = %q, want tool name %q", req.Command, "retrieval.search")
				}
				if req.Path != "" || len(req.Args) != 0 {
					t.Errorf("custom request must not carry Path/Args: %+v", req)
				}
			},
		},
		{
			name:     "unknown tool",
			toolName: "unknown.tool",
			args:     map[string]any{},
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := BuildPermsRequest(tc.toolName, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if tc.checkFunc != nil {
					tc.checkFunc(t, req)
				}
			}
		})
	}
}
