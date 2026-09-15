package agent

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// TestExecuteTurnWithOptions_NoToolsStripsToolDefs confirms TurnOptions.NoTools
// sends an empty ChatRequest.Tools even though the registry has real tools
// registered — the hard technical constraint behind the RF-11 manifest
// decomposition turn (internal/run/decompose.go), added after a real
// decomposition call against this repo exhausted max_iterations exploring
// the filesystem instead of answering, despite the prompt asking it not to.
func TestExecuteTurnWithOptions_NoToolsStripsToolDefs(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	var gotTools []llm.ToolDef
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			gotTools = req.Tools
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "[]"}}},
				Usage:   &llm.Usage{TotalTokens: 10},
			}, nil
		}),
	}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil) // non-empty registry — proves NoTools actually suppresses it
	logger := newTestLogger()
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	_, err := agent.ExecuteTurnWithOptions(ctx, "session-1", "propose a plan", TurnOptions{NoTools: true})
	if err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v", err)
	}
	if len(gotTools) != 0 {
		t.Errorf("ChatRequest.Tools = %d entries, want 0 (NoTools must strip them)", len(gotTools))
	}
}

// TestExecuteTurnWithOptions_ToolsPresentByDefault is the control case: the
// same registry, without NoTools, DOES send tool defs — proving the
// previous test's empty result is NoTools' doing, not an empty registry.
func TestExecuteTurnWithOptions_ToolsPresentByDefault(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	var gotTools []llm.ToolDef
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			gotTools = req.Tools
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "ok"}}},
				Usage:   &llm.Usage{TotalTokens: 10},
			}, nil
		}),
	}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	_, err := agent.ExecuteTurn(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(gotTools) == 0 {
		t.Error("expected tool defs to be present without NoTools")
	}
}
