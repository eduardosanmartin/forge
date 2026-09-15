package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// preChannelFailProvider fails ChatStream before any channel is created —
// the shape of a real connection-refused/429 failure (see
// OpenAICompatibleProvider.ChatStream: p.mapError/p.mapHTTPError wrap it as
// a *llm.RetryableError and return before a channel ever exists). This is
// distinct from streamingMockProvider's streamError field, which fails via a
// StreamChunk{Error: ...} sent on an open channel — that shape becomes a
// plain fmt.Errorf in consumeStream, never IsRetryable, so it cannot
// exercise callLLMStreamWithFailover's retry condition.
type preChannelFailProvider struct {
	err error
}

func (p *preChannelFailProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, p.err
}
func (p *preChannelFailProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, p.err
}
func (p *preChannelFailProvider) ListModels() ([]string, error) { return nil, nil }
func (p *preChannelFailProvider) Close() error                  { return nil }

// streamFailoverRegistry implements LLMRegistryInterface plus
// fallbackChainSource so ExecuteTurn's streaming path can exercise
// callLLMStreamWithFailover end to end, the way llm.Registry does for real.
type streamFailoverRegistry struct {
	defaultProvider llm.Provider
	defaultModel    string
	chain           []llm.FallbackTarget
}

func (r *streamFailoverRegistry) GetDefault() (llm.Provider, string) {
	return r.defaultProvider, r.defaultModel
}
func (r *streamFailoverRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.defaultProvider.Chat(ctx, req)
}
func (r *streamFailoverRegistry) FallbackChain() []llm.FallbackTarget { return r.chain }

// TestExecuteTurn_StreamingFailsOverBeforeFirstToken reproduces the exact
// live-daemon scenario that motivated this test: the default provider's
// ChatStream fails immediately (connection refused, wrapped as
// *llm.RetryableError) before any token streams — nothing has reached the
// caller yet, so it's safe to retry the next fallback_chain entry, same as
// the non-streaming path.
func TestExecuteTurn_StreamingFailsOverBeforeFirstToken(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.LLM.Streaming = config.StreamingConfig{Mode: config.StreamingModeOn}
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)

	broken := &preChannelFailProvider{err: &llm.RetryableError{Err: errors.New("connection refused")}}
	good := &streamingMockProvider{content: "hi from backup", finish: "stop"}

	reg := &streamFailoverRegistry{
		defaultProvider: broken,
		defaultModel:    "broken-model",
		chain: []llm.FallbackTarget{
			{Provider: good, ProviderName: "backup", Model: "good-model"},
		},
	}

	ag := NewAgent(cfg, storeImpl, reg, toolsReg, permsEng, newTestLogger())
	var deltas []string
	result, err := ag.ExecuteTurnWithOptions(ctx, "session-1", "hello", TurnOptions{
		StreamingEnabled: true,
		OnDelta:          func(d string) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("result.Error = %v, want nil (should have fallen over to the backup model)", result.Error)
	}
	final := result.Messages[len(result.Messages)-1].Content
	if final != "hi from backup" {
		t.Errorf("final content = %q, want \"hi from backup\"", final)
	}
}

// TestExecuteTurn_StreamingNoFailoverWithoutChainSource confirms a registry
// that only implements ChatStream (no fallbackChainSource) degrades to a
// single attempt — the pre-existing behavior for every registry built before
// this feature.
func TestExecuteTurn_StreamingNoFailoverWithoutChainSource(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.LLM.Streaming = config.StreamingConfig{Mode: config.StreamingModeOn}
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)

	broken := &preChannelFailProvider{err: &llm.RetryableError{Err: errors.New("connection refused")}}
	reg := &streamingMockRegistry{provider: broken}

	ag := NewAgent(cfg, storeImpl, reg, toolsReg, permsEng, newTestLogger())
	_, err := ag.ExecuteTurnWithOptions(ctx, "session-1", "hello", TurnOptions{StreamingEnabled: true})
	if err == nil {
		t.Fatal("expected the turn to fail: no fallback_chain source available, single attempt only")
	}
}
