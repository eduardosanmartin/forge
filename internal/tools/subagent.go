// Package tools implements the spawn_subagent first-class tool (RF-1.3).
package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// contextKey is the private key for session-scoped tool context.
type contextKey string

const sessionIDKey contextKey = "forge.session_id"

// WithSessionID returns a context carrying the parent session ID for spawn tools.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext extracts the parent session ID or "" if absent.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey).(string); ok {
		return v
	}
	return ""
}

// SpawnFunc is the daemon/agent-provided spawner (injected to avoid import cycles).
type SpawnFunc func(ctx context.Context, task string, maxIterations int, tokenBudget int, fileBudget string) (Result, error)

type spawnSubagentTool struct {
	mu      sync.RWMutex
	spawner SpawnFunc
}

func NewSpawnSubagentTool() *spawnSubagentTool {
	return &spawnSubagentTool{}
}

// NewSpawnSubagentToolWithSpawner creates a tool with an initial spawner (test helper).
func NewSpawnSubagentToolWithSpawner(spawner SpawnFunc) *spawnSubagentTool {
	return &spawnSubagentTool{spawner: spawner}
}

// SetSpawner injects or replaces the spawner (called by SessionManager after wiring).
func (t *spawnSubagentTool) SetSpawner(spawner SpawnFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.spawner = spawner
}

func (t *spawnSubagentTool) Name() string { return "spawn_subagent" }

func (t *spawnSubagentTool) Description() string {
	return "Spawn a bounded child agent turn with isolated context (task + iteration/token/file budget). Child runs the same agent loop in a branched session inheriting the parent permission floor (never wider); its tool calls are fenced. Returns the child's summarized result for the parent to consume."
}

func (t *spawnSubagentTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "Bounded task description for the child turn",
			},
			"max_iterations": map[string]any{
				"type":        "number",
				"description": "Max tool-call iterations for the child (0 = parent default, clamped to parent ceiling)",
			},
			"token_budget": map[string]any{
				"type":        "number",
				"description": "Token budget hint for the child (0 = no explicit cap)",
			},
			"file_budget": map[string]any{
				"type":        "string",
				"description": "Optional file scope hint for the child",
			},
		},
		"required": []string{"task"},
	}
}

func (t *spawnSubagentTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	t.mu.RLock()
	spawner := t.spawner
	t.mu.RUnlock()
	if spawner == nil {
		return Result{Content: "ERROR: subagent spawner not configured"}, nil
	}
	args := req.Input
	if args == nil {
		args = map[string]any{}
	}
	task, _ := args["task"].(string)
	if task == "" {
		return Result{Content: "ERROR: task is required"}, nil
	}
	maxIter := 0
	if v, ok := args["max_iterations"]; ok {
		if f, ok := v.(float64); ok {
			maxIter = int(f)
		} else if i, ok := v.(int); ok {
			maxIter = i
		} else if i64, ok := v.(int64); ok {
			maxIter = int(i64)
		}
	}
	tokenBudget := 0
	if v, ok := args["token_budget"]; ok {
		if f, ok := v.(float64); ok {
			tokenBudget = int(f)
		} else if i, ok := v.(int); ok {
			tokenBudget = i
		} else if i64, ok := v.(int64); ok {
			tokenBudget = int(i64)
		}
	}
	fileBudget, _ := args["file_budget"].(string)

	// Validate via spawner; just forward.
	result, err := spawner(ctx, task, maxIter, tokenBudget, fileBudget)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}
	return result, nil
}
