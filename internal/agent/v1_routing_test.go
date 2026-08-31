// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
//
// This file proves the v1 routing flag changes real behavior in the agent
// loop: with the flag on and a registry that can resolve step models, the
// main generation call uses the router's model; with the flag off, the
// default model is used. No new LLM calls are introduced — only the model
// name of the existing call changes.
package agent

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/routing"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// routingLLMRegistry extends the plain mock registry with GetModelForStep,
// mimicking llm.Registry's router-backed step resolution.
type routingLLMRegistry struct {
	*mockLLMRegistry
	routed map[routing.StepType]string
}

func (m *routingLLMRegistry) GetModelForStep(step routing.StepType) string {
	return m.routed[step]
}

func TestAgent_ExecuteTurn_RoutingEnabledSelectsGenerationModel(t *testing.T) {
	ctx := context.Background()

	var gotModel string
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		gotModel = req.Model
		return llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.Message{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		}, nil
	})

	storeImpl := newMockStore()
	storeImpl.sessions["session-1"] = &store.Session{
		ID:       "session-1",
		Metadata: map[string]any{"v1_routing": true},
	}
	llmReg := &routingLLMRegistry{
		mockLLMRegistry: &mockLLMRegistry{provider: provider},
		routed: map[routing.StepType]string{
			routing.StepGenerate: "routed-generation-model",
		},
	}

	agent := NewAgent(config.Defaults(), storeImpl, llmReg,
		tools.NewDefaultRegistry(newTestPermsEngine(t), "", nil),
		newTestPermsEngine(t), newTestLogger())

	result, err := agent.ExecuteTurn(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected user + assistant messages, got %d", len(result.Messages))
	}
	if gotModel != "routed-generation-model" {
		t.Errorf("ChatRequest.Model = %q, want the routed generation model %q", gotModel, "routed-generation-model")
	}
}

func TestAgent_ExecuteTurn_RoutingDisabledUsesDefaultModel(t *testing.T) {
	ctx := context.Background()

	var gotModel string
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		gotModel = req.Model
		return llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.Message{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		}, nil
	})

	// No v1_routing metadata: default model expected (mock GetDefault
	// reports "test-model").
	storeImpl := newMockStore()
	llmReg := &routingLLMRegistry{
		mockLLMRegistry: &mockLLMRegistry{provider: provider},
		routed: map[routing.StepType]string{
			routing.StepGenerate: "routed-generation-model",
		},
	}

	agent := NewAgent(config.Defaults(), storeImpl, llmReg,
		tools.NewDefaultRegistry(newTestPermsEngine(t), "", nil),
		newTestPermsEngine(t), newTestLogger())

	if _, err := agent.ExecuteTurn(ctx, "session-1", "hello"); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if gotModel != "test-model" {
		t.Errorf("ChatRequest.Model = %q, want the default model %q", gotModel, "test-model")
	}
}

// TestAgent_ExecuteTurn_RoutingEnabledFallsBackToDefaultModel covers a
// registry whose router cannot resolve the step (empty model): the loop
// must fall back to the default model instead of sending an empty one.
func TestAgent_ExecuteTurn_RoutingEnabledFallsBackToDefaultModel(t *testing.T) {
	ctx := context.Background()

	var gotModel string
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		gotModel = req.Model
		return llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.Message{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		}, nil
	})

	storeImpl := newMockStore()
	storeImpl.sessions["session-1"] = &store.Session{
		ID:       "session-1",
		Metadata: map[string]any{"v1_routing": true},
	}
	llmReg := &routingLLMRegistry{
		mockLLMRegistry: &mockLLMRegistry{provider: provider},
		routed:          map[routing.StepType]string{}, // resolves nothing
	}

	agent := NewAgent(config.Defaults(), storeImpl, llmReg,
		tools.NewDefaultRegistry(newTestPermsEngine(t), "", nil),
		newTestPermsEngine(t), newTestLogger())

	if _, err := agent.ExecuteTurn(ctx, "session-1", "hello"); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if gotModel != "test-model" {
		t.Errorf("ChatRequest.Model = %q, want fallback default model %q", gotModel, "test-model")
	}
}
