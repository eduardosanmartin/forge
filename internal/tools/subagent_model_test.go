package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// capturingSpawner records the SpawnRequest it receives; err, if set, is
// returned as the spawner's error (mirroring agent.SpawnChild validation).
type capturingSpawner struct {
	req SpawnRequest
	err error
}

func (c *capturingSpawner) spawn(ctx context.Context, req SpawnRequest) (Result, error) {
	c.req = req
	if c.err != nil {
		return Result{}, c.err
	}
	return Result{Content: "child summary", Metadata: map[string]any{"ok": true}}, nil
}

func TestSpawnSubagentTool_ProviderModelArgsForwarded(t *testing.T) {
	table := []struct {
		name         string
		input        map[string]any
		wantProvider string
		wantModel    string
	}{
		{
			name:         "provider and model forwarded",
			input:        map[string]any{"task": "t", "provider": "alpha", "model": "m1"},
			wantProvider: "alpha",
			wantModel:    "m1",
		},
		{
			name:         "model only (fanout default-provider convention)",
			input:        map[string]any{"task": "t", "model": "m2"},
			wantProvider: "",
			wantModel:    "m2",
		},
		{
			name:  "neither set inherits defaults",
			input: map[string]any{"task": "t"},
		},
		{
			name:         "non-string model is ignored, not panicked on",
			input:        map[string]any{"task": "t", "model": float64(7)},
			wantProvider: "",
			wantModel:    "",
		},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			cap := &capturingSpawner{}
			tool := NewSpawnSubagentToolWithSpawner(cap.spawn)
			ctx := WithSessionID(context.Background(), "parent-1")
			req := perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent", Input: tc.input}
			res, err := tool.Execute(ctx, req)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Content != "child summary" {
				t.Fatalf("content %q", res.Content)
			}
			if cap.req.Provider != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", cap.req.Provider, tc.wantProvider)
			}
			if cap.req.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", cap.req.Model, tc.wantModel)
			}
		})
	}
}

// TestSpawnSubagentTool_UnknownProviderYieldsERRORResult pins the RF-9.3
// contract: the agent-level validation error surfaces as an ERROR-quoted
// tool result, never a panic.
func TestSpawnSubagentTool_UnknownProviderYieldsERRORResult(t *testing.T) {
	cap := &capturingSpawner{err: errors.New("spawn child: unknown provider \"ghost\"")}
	tool := NewSpawnSubagentToolWithSpawner(cap.spawn)
	req := perms.Request{Kind: perms.KindCustom, Command: "spawn_subagent",
		Input: map[string]any{"task": "t", "provider": "ghost"}}
	res, err := tool.Execute(WithSessionID(context.Background(), "parent-1"), req)
	if err != nil {
		t.Fatalf("tool must not bubble the error, got %v", err)
	}
	if !strings.Contains(res.Content, "ERROR:") || !strings.Contains(res.Content, "unknown provider") {
		t.Fatalf("want ERROR-quoted unknown provider result, got %q", res.Content)
	}
}

func TestSpawnSubagentTool_SchemaExposesProviderAndModel(t *testing.T) {
	tool := NewSpawnSubagentTool()
	props, ok := tool.JSONSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	for _, key := range []string{"provider", "model"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("schema property %q (RF-9.3) missing", key)
		}
	}
}
