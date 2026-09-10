package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// hangingProvider blocks until the turn context (or the test escape) ends,
// simulating a hung upstream like the retest-5 OpenRouter stall.
func hangingProvider(testCtx context.Context) *mockLLMRegistry {
	return &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return llm.ChatResponse{}, ctx.Err()
			case <-testCtx.Done():
				return llm.ChatResponse{}, errors.New("test escape: turn did not time out")
			}
		}),
	}
}

func TestNewAgentMaxTurnSecondsFromConfig(t *testing.T) {
	cfg := config.Defaults()
	if cfg.Agent.MaxTurnSeconds != config.DefaultAgentMaxTurnSeconds {
		t.Fatalf("defaults want %d, got %d", config.DefaultAgentMaxTurnSeconds, cfg.Agent.MaxTurnSeconds)
	}
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent.maxTurnSeconds != config.DefaultAgentMaxTurnSeconds {
		t.Fatalf("NewAgent default want %d, got %d", config.DefaultAgentMaxTurnSeconds, agent.maxTurnSeconds)
	}
	cfg2 := config.Defaults()
	cfg2.Agent.MaxTurnSeconds = 60
	agent2 := NewAgent(cfg2, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent2.maxTurnSeconds != 60 {
		t.Fatalf("NewAgent override want 60, got %d", agent2.maxTurnSeconds)
	}
	// cfg nil should keep default
	agent3 := NewAgent(nil, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent3.maxTurnSeconds != config.DefaultAgentMaxTurnSeconds {
		t.Fatalf("nil cfg want %d, got %d", config.DefaultAgentMaxTurnSeconds, agent3.maxTurnSeconds)
	}
	// zero in cfg falls back to default (mirrors max_iterations wiring)
	cfg4 := &config.Config{}
	agent4 := NewAgent(cfg4, storeImpl, newMockLLMRegistry(nil), toolsReg, permsEng, newTestLogger())
	if agent4.maxTurnSeconds != config.DefaultAgentMaxTurnSeconds {
		t.Fatalf("zero cfg should fallback to %d, got %d", config.DefaultAgentMaxTurnSeconds, agent4.maxTurnSeconds)
	}
}

func TestAgentTurnTimeoutAbortsHungProvider(t *testing.T) {
	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, hangingProvider(testCtx), toolsReg, permsEng, newTestLogger())
	start := time.Now()
	res, err := agent.ExecuteTurnWithOptions(context.Background(), "session-1", "hello", TurnOptions{Timeout: 100 * time.Millisecond})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected turn timeout error")
	}
	if !strings.Contains(err.Error(), "turn aborted: turn timeout after 100ms") {
		t.Fatalf("error should name the timeout, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "raise agent.max_turn_seconds in .forge/config.json") {
		t.Fatalf("error should contain guidance, got %q", err.Error())
	}
	if !res.Halted {
		t.Fatal("timed-out turn must be halted")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("turn should fail fast on timeout, took %v", elapsed)
	}
}

func TestAgentTurnTimeoutDefaultFromConfig(t *testing.T) {
	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := config.Defaults()
	cfg.Agent.MaxTurnSeconds = 1
	storeImpl := newMockStore()
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	agent := NewAgent(cfg, storeImpl, hangingProvider(testCtx), toolsReg, permsEng, newTestLogger())
	start := time.Now()
	_, err := agent.ExecuteTurn(context.Background(), "session-1", "hello")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected turn timeout error")
	}
	if !strings.Contains(err.Error(), "turn aborted: turn timeout after 1s") {
		t.Fatalf("error should name the configured timeout, got %q", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("turn should fail fast on timeout, took %v", elapsed)
	}
}
