package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestNewAgentMaxIterationsFromConfig(t *testing.T) {
	cfg := config.Defaults()
	if cfg.Agent.MaxIterations != 10 {
		t.Fatalf("defaults want 10, got %d", cfg.Agent.MaxIterations)
	}
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent.maxIterations != 10 {
		t.Fatalf("NewAgent default want 10, got %d", agent.maxIterations)
	}
	cfg2 := config.Defaults()
	cfg2.Agent.MaxIterations = 3
	agent2 := NewAgent(cfg2, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent2.maxIterations != 3 {
		t.Fatalf("NewAgent override want 3, got %d", agent2.maxIterations)
	}
	// cfg nil should keep default 10
	agent3 := NewAgent(nil, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent3.maxIterations != 10 {
		t.Fatalf("nil cfg want 10, got %d", agent3.maxIterations)
	}
	// zero/negative in cfg should fallback to default via Load, but direct construction bypasses Load normalization
	cfg4 := &config.Config{}
	cfg4.Agent.MaxIterations = 0
	agent4 := NewAgent(cfg4, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent4.maxIterations != 10 {
		t.Fatalf("zero cfg should fallback to 10, got %d", agent4.maxIterations)
	}
}

func TestAgentMaxIterationsAbortMessage(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Agent.MaxIterations = 2
	storeImpl := newMockStore()
	// Always return tool calls to hit max
	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			return llm.ChatResponse{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role:    "assistant",
						Content: "retry",
						ToolCalls: []llm.ToolCall{{
							ID:   "c",
							Type: "function",
							Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"x"}`},
						}},
					},
				}},
				Usage: &llm.Usage{TotalTokens: 1},
			}, nil
		}),
	}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	res, err := agent.ExecuteTurn(ctx, "session-1", "hello")
	if err == nil {
		t.Fatal("expected max_iterations error")
	}
	if !strings.Contains(err.Error(), "turn aborted: agent reached max_iterations (2)") {
		t.Fatalf("error message should contain new guidance, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "raise agent.max_iterations in .forge/config.json") {
		t.Fatalf("error should contain guidance, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "after 2 iterations") {
		t.Fatalf("error should surface iterations ran, got %q", err.Error())
	}
	if res.Metrics.IterationCount != 3 {
		t.Fatalf("IterationCount want 3 (2 limit +1), got %d", res.Metrics.IterationCount)
	}
}


