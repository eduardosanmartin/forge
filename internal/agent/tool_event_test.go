package agent

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

type toolEventCall struct {
	toolCallID, name, status, errMsg string
}

// TestAgent_OnToolEvent_SerialPath verifies OnToolEvent fires exactly
// started -> finished around a single tool call on the normal (non-parallel)
// execution path — the mechanism `forge run` subscribes to for live
// per-tool-call progress ticks during a manifest task's turn.
func TestAgent_OnToolEvent_SerialPath(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return llm.ChatResponse{
					Choices: []llm.Choice{{Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							// fs_list on "." always succeeds regardless of
							// cwd (unlike fs_read on a specific file, which
							// would need that exact file to exist on disk).
							ID: "call-1", Type: "function",
							Function: llm.ToolCallFunction{Name: "fs_list", Arguments: `{"path": "."}`},
						}},
					}}},
					Usage: &llm.Usage{TotalTokens: 10},
				}, nil
			}
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}},
				Usage:   &llm.Usage{TotalTokens: 10},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()
	agentObj := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	var events []toolEventCall
	opts := TurnOptions{
		OnToolEvent: func(toolCallID, name, status, errMsg string) {
			events = append(events, toolEventCall{toolCallID, name, status, errMsg})
		},
	}
	if _, err := agentObj.ExecuteTurnWithOptions(ctx, "session-1", "List files", opts); err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v", err)
	}

	want := []toolEventCall{
		{"call-1", "fs_list", "started", ""},
		{"call-1", "fs_list", "finished", ""},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d tool events, want %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		if events[i] != w {
			t.Errorf("event[%d] = %+v, want %+v", i, events[i], w)
		}
	}
}

// TestAgent_OnToolEvent_ToolFailure verifies a tool that returns an error
// still fires started, then "error" (not "finished") with the error text —
// so a live progress display can distinguish a failing tool from a slow one.
func TestAgent_OnToolEvent_ToolFailure(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return llm.ChatResponse{
					Choices: []llm.Choice{{Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID: "call-1", Type: "function",
							// no_such_tool is never registered -> toolsReg.Execute errors.
							Function: llm.ToolCallFunction{Name: "no_such_tool", Arguments: `{}`},
						}},
					}}},
					Usage: &llm.Usage{TotalTokens: 10},
				}, nil
			}
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}},
				Usage:   &llm.Usage{TotalTokens: 10},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()
	agentObj := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	var events []toolEventCall
	opts := TurnOptions{
		OnToolEvent: func(toolCallID, name, status, errMsg string) {
			events = append(events, toolEventCall{toolCallID, name, status, errMsg})
		},
	}
	if _, err := agentObj.ExecuteTurnWithOptions(ctx, "session-1", "do the thing", opts); err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d tool events, want 2: %+v", len(events), events)
	}
	if events[0].status != "started" {
		t.Errorf("event[0].status = %q, want started", events[0].status)
	}
	if events[1].status != "error" {
		t.Errorf("event[1].status = %q, want error", events[1].status)
	}
	if events[1].errMsg == "" {
		t.Error("error event should carry a non-empty error message")
	}
}
