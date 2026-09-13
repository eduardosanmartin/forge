// Package tools implements the spawn_subagent first-class tool (RF-1.3).
package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// intArg reads a numeric tool argument that JSON decoding may deliver in
// any of the expected numeric shapes.
func intArg(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

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
type SpawnFunc func(ctx context.Context, req SpawnRequest) (Result, error)

// SpawnRequest carries the spawn_subagent tool arguments to the injected
// spawner. Provider/Model are RF-9.3 fanout overrides; empty means the
// child inherits the canonical default provider/model.
type SpawnRequest struct {
	Task          string
	MaxIterations int
	TokenBudget   int
	FileBudget    string
	Provider      string
	Model         string
}

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
			"provider": map[string]any{
				"type":        "string",
				"description": "Optional named LLM provider override (RF-9.3 fanout); unknown names fail cleanly instead of falling back",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional model override for the child turn (RF-9.3 fanout); empty = canonical default model",
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
	sr := SpawnRequest{}
	sr.Task, _ = args["task"].(string)
	if sr.Task == "" {
		return Result{Content: "ERROR: task is required"}, nil
	}
	sr.MaxIterations = intArg(args, "max_iterations")
	sr.TokenBudget = intArg(args, "token_budget")
	sr.FileBudget, _ = args["file_budget"].(string)
	sr.Provider, _ = args["provider"].(string)
	sr.Model, _ = args["model"].(string)

	// Validate via spawner; unknown provider/model names surface there as
	// ERROR-quoted tool results (never a panic — the spawner contract).
	result, err := spawner(ctx, sr)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}
	return result, nil
}
