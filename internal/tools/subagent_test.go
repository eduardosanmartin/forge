package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

func TestSpawnSubagentTool_Schema(t *testing.T) {
	tool := NewSpawnSubagentTool()
	schema := tool.JSONSchema()
	if schema["type"] != "object" {
		t.Fatalf("schema type")
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["task"]; !ok {
		t.Fatal("task prop missing")
	}
	req, _ := schema["required"].([]string)
	found := false
	for _, r := range req {
		if r == "task" {
			found = true
		}
	}
	if !found {
		t.Fatal("task required")
	}
}

func TestSpawnSubagentTool_MissingSpawner(t *testing.T) {
	tool := NewSpawnSubagentTool()
	req := perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent", Input: map[string]any{"task": "hello"}}
	res, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if !strings.Contains(res.Content, "spawner not configured") {
		t.Fatalf("want spawner not configured, got %q", res.Content)
	}
}

func TestSpawnSubagentTool_WithSpawner(t *testing.T) {
	called := false
	tool := NewSpawnSubagentToolWithSpawner(func(ctx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (Result, error) {
		called = true
		if task != "child task" {
			t.Fatalf("task %q", task)
		}
		if maxIter != 3 {
			t.Fatalf("maxIter %d", maxIter)
		}
		if tokenBudget != 100 {
			t.Fatalf("tokenBudget %d", tokenBudget)
		}
		if fileBudget != "foo.go" {
			t.Fatalf("fileBudget %q", fileBudget)
		}
		if SessionIDFromContext(ctx) != "parent-1" {
			t.Fatalf("session id not propagated got %q", SessionIDFromContext(ctx))
		}
		return Result{Content: "child summary", Metadata: map[string]any{"ok": true}}, nil
	})
	ctx := WithSessionID(context.Background(), "parent-1")
	req := perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent", Input: map[string]any{"task": "child task", "max_iterations": float64(3), "token_budget": float64(100), "file_budget": "foo.go"}}
	res, err := tool.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !called {
		t.Fatal("spawner not called")
	}
	if res.Content != "child summary" {
		t.Fatalf("content %q", res.Content)
	}
}

func TestSpawnSubagentTool_EmptyTask(t *testing.T) {
	tool := NewSpawnSubagentToolWithSpawner(func(ctx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (Result, error) {
		t.Fatal("should not be called for empty task")
		return Result{}, nil
	})
	req := perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent", Input: map[string]any{"task": ""}}
	res, _ := tool.Execute(context.Background(), req)
	if !strings.Contains(res.Content, "task is required") {
		t.Fatalf("want task required, got %q", res.Content)
	}
}

func TestSpawnSubagentTool_RegistryIntegration(t *testing.T) {
	// Registry with custom floor (allow) must allow spawn_subagent without policy change.
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
	}
	eng, err := perms.New(policy, tmp, nil)
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	reg := New(eng, tmp, nil)
	for _, tl := range defaultRegistryTools(nil) {
		reg.Register(tl)
	}
	tool := NewSpawnSubagentToolWithSpawner(func(ctx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (Result, error) {
		return Result{Content: "via-registry ok"}, nil
	})
	reg.Register(tool)
	// Schema validation + perms check + fencing should succeed.
	res, err := reg.Execute(WithSessionID(context.Background(), "s1"), "spawn_subagent", map[string]any{"task": "hello"})
	if err != nil {
		t.Fatalf("registry execute: %v", err)
	}
	if !strings.Contains(res.Content, "via-registry ok") {
		t.Fatalf("want fenced via-registry ok, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "<<TOOL_RESULT:spawn_subagent>>") {
		t.Fatalf("want fenced, got %q", res.Content)
	}
}

func TestSessionIDContext(t *testing.T) {
	ctx := context.Background()
	if SessionIDFromContext(ctx) != "" {
		t.Fatal("empty context should yield empty")
	}
	ctx2 := WithSessionID(ctx, "abc")
	if SessionIDFromContext(ctx2) != "abc" {
		t.Fatal("roundtrip failed")
	}
}
