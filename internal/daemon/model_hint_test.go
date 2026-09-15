package daemon

import (
	"context"
	"log/slog"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/routing"
)

// modelCapturingProvider records the model name of the last Chat request it
// received, so a test can confirm which model a turn actually used.
type modelCapturingProvider struct {
	lastModel string
}

func (p *modelCapturingProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.lastModel = req.Model
	return llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   &llm.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
	}, nil
}
func (p *modelCapturingProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, llm.ErrStreamingNotSupported
}
func (p *modelCapturingProvider) ListModels() ([]string, error) { return []string{"default-model"}, nil }
func (p *modelCapturingProvider) Close() error                  { return nil }

// routedTestLLMRegistry is a minimal LLMRegistryInterface + routerProvider
// implementation: its GetRouter() lets ExecuteTurnWithModelHint resolve a
// role hint to a concrete model, unlike the plain testLLMRegistry used by
// most other session_mgr tests (which has no router support at all — the
// "hint resolves to nothing, turn behaves exactly like ExecuteTurn" case).
type routedTestLLMRegistry struct {
	provider *modelCapturingProvider
	router   *routing.ModelRouter
}

func (r *routedTestLLMRegistry) GetDefault() (llm.Provider, string)     { return r.provider, "default-model" }
func (r *routedTestLLMRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.provider.Chat(ctx, req)
}
func (r *routedTestLLMRegistry) Close() error                        { return nil }
func (r *routedTestLLMRegistry) GetRouter() *routing.ModelRouter     { return r.router }

func newRoutedTestLLMRegistry() *routedTestLLMRegistry {
	router := routing.NewModelRouter(map[routing.ModelRole]string{
		routing.RoleCheap:      "cheap-model",
		routing.RoleGeneration: "generation-model",
		routing.RoleReasoning:  "reasoning-model",
	})
	return &routedTestLLMRegistry{provider: &modelCapturingProvider{}, router: router}
}

func TestExecuteTurnWithModelHint_ResolvesRoleToModel(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newRoutedTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)
	session, err := mgr.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := mgr.ExecuteTurnWithModelHint(context.Background(), session.ID, "do a cheap thing", "cheap"); err != nil {
		t.Fatalf("ExecuteTurnWithModelHint: %v", err)
	}
	if llmReg.provider.lastModel != "cheap-model" {
		t.Errorf("model used = %q, want %q (resolved from hint %q)", llmReg.provider.lastModel, "cheap-model", "cheap")
	}
}

func TestExecuteTurnWithModelHint_EmptyHintUsesDefault(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newRoutedTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)
	session, err := mgr.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := mgr.ExecuteTurnWithModelHint(context.Background(), session.ID, "hello", ""); err != nil {
		t.Fatalf("ExecuteTurnWithModelHint: %v", err)
	}
	if llmReg.provider.lastModel != "default-model" {
		t.Errorf("model used = %q, want the session default %q (empty hint must not override anything)", llmReg.provider.lastModel, "default-model")
	}
}

// TestExecuteTurnWithModelHint_NoRouterSupportDegradesGracefully confirms a
// registry without router support (the common testLLMRegistry used
// elsewhere) doesn't fail the turn over an unresolvable hint — it just runs
// with no override, same as ExecuteTurn.
func TestExecuteTurnWithModelHint_NoRouterSupportDegradesGracefully(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry() // no GetRouter() method at all
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)
	session, err := mgr.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := mgr.ExecuteTurnWithModelHint(context.Background(), session.ID, "hello", "reasoning"); err != nil {
		t.Fatalf("ExecuteTurnWithModelHint should degrade gracefully, got error: %v", err)
	}
}
