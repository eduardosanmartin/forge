package agent

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// failoverTrackingRegistry implements agent.LLMRegistryInterface AND
// chatFailover (ChatWithFallback) so a test can tell whether the loop used
// the failover-aware path or called provider.Chat directly.
type failoverTrackingRegistry struct {
	provider           *mockProvider
	fallbackCalled     bool
	plainChatCalled    bool
	fallbackCalledWith llm.ChatRequest
}

func (r *failoverTrackingRegistry) GetDefault() (llm.Provider, string) {
	return r.provider, "default-model"
}
func (r *failoverTrackingRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	r.plainChatCalled = true
	return r.provider.Chat(ctx, req)
}
func (r *failoverTrackingRegistry) ChatWithFallback(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	r.fallbackCalled = true
	r.fallbackCalledWith = req
	return r.provider.Chat(ctx, req)
}

func fixedResponseProvider(content string) *mockProvider {
	return newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: content}, FinishReason: "stop"}},
			Usage:   &llm.Usage{TotalTokens: 10},
		}, nil
	})
}

// TestExecuteTurn_PlainDefaultUsesChatWithFallback confirms the loop's
// plain-default path (no override, no routing) calls ChatWithFallback when
// the registry supports it — the wiring this session's Fase 3 added. Before
// this wiring, ChatWithFallback existed but nothing ever called it for a
// real turn.
func TestExecuteTurn_PlainDefaultUsesChatWithFallback(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	reg := &failoverTrackingRegistry{provider: fixedResponseProvider("ok")}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, reg, toolsReg, permsEng, newTestLogger())

	if _, err := agent.ExecuteTurn(ctx, "session-1", "hello"); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !reg.fallbackCalled {
		t.Error("ChatWithFallback was not called on the plain-default path")
	}
	if reg.plainChatCalled {
		t.Error("Chat (non-failover) was called too — plain default should go through ChatWithFallback only")
	}
}

// TestExecuteTurn_OverrideModelSkipsFailover confirms an explicit per-turn
// model override (opts.OverrideModel — how RF-11 manifest task model_hint
// reaches a turn) bypasses ChatWithFallback: an explicit pin is deliberate
// intent, not something a fallback chain should silently override.
func TestExecuteTurn_OverrideModelSkipsFailover(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	reg := &failoverTrackingRegistry{provider: fixedResponseProvider("ok")}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, reg, toolsReg, permsEng, newTestLogger())

	if _, err := agent.ExecuteTurnWithOptions(ctx, "session-1", "hello", TurnOptions{OverrideModel: "pinned-model"}); err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v", err)
	}
	if reg.fallbackCalled {
		t.Error("ChatWithFallback was called despite an explicit OverrideModel — overrides must bypass failover")
	}
	// Note: an override reaches the resolved provider directly
	// (provider.Chat), not via the registry's own Chat method — so
	// reg.plainChatCalled isn't the right signal here; not calling
	// ChatWithFallback (asserted above) already proves the override path
	// was taken, since the turn completed successfully either way.
}
