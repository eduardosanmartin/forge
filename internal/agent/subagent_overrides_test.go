package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// recordingProvider is a named provider mock that records every Chat call,
// so tests can prove which provider served a child turn and with which model.
type recordingProvider struct {
	name string
	mu   sync.Mutex
	reqs []llm.ChatRequest
}

func (p *recordingProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	content := "served by " + p.name + " model " + req.Model
	return llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: content}}},
		Usage:   &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func (p *recordingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, llm.ErrStreamingNotSupported
}

func (p *recordingProvider) ListModels() ([]string, error) { return []string{"m1", "m2"}, nil }
func (p *recordingProvider) Close() error                  { return nil }

func (p *recordingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// namedMockRegistry exposes two distinct named providers plus a default,
// satisfying agent.LLMRegistryInterface + ProviderResolver (like llm.Registry).
type namedMockRegistry struct {
	defaultProvider *recordingProvider
	named           map[string]*recordingProvider
}

func (r *namedMockRegistry) GetDefault() (llm.Provider, string) {
	return r.defaultProvider, "default-model"
}

func (r *namedMockRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.defaultProvider.Chat(ctx, req)
}

func (r *namedMockRegistry) GetProvider(name string) (llm.Provider, bool) {
	p, ok := r.named[name]
	return p, ok
}

// newTwoProviderEnv wires an agent over a branch store with two named
// recording providers (alpha, beta) and a default provider.
func newTwoProviderEnv(t *testing.T) (*Agent, *branchMockStore, *namedMockRegistry) {
	t.Helper()
	cfg := config.Defaults()
	storeImpl := newBranchMockStore()
	reg := &namedMockRegistry{
		defaultProvider: &recordingProvider{name: "default"},
		named: map[string]*recordingProvider{
			"alpha": {name: "alpha"},
			"beta":  {name: "beta"},
		},
	}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	return NewAgent(cfg, storeImpl, reg, toolsReg, permsEng, newTestLogger()), storeImpl, reg
}

func TestSpawnChild_NamedProviderAndModelOverrides(t *testing.T) {
	ctx := context.Background()
	ag, storeImpl, reg := newTwoProviderEnv(t)

	results, errs := ag.SpawnChildren(ctx, "parent-1", []ChildSpec{
		{Task: "alpha work", Provider: "alpha", Model: "m1"},
		{Task: "beta work", Provider: "beta", Model: "m2"},
	})
	for _, err := range errs {
		if err != nil {
			t.Fatalf("SpawnChildren error: %v", err)
		}
	}
	if len(results) != 2 {
		t.Fatalf("want 2 children, got %d", len(results))
	}
	for i, res := range results {
		if !res.Success {
			t.Fatalf("child %d failed: %s", i, res.Error)
		}
		if res.ChildSessionID == "" || res.ChildSessionID == "parent-1" {
			t.Fatalf("child %d session id invalid", i)
		}
	}

	if got, want := reg.named["alpha"].callCount(), 1; got != want {
		t.Fatalf("alpha Chat calls = %d, want %d", got, want)
	}
	if got, want := reg.named["beta"].callCount(), 1; got != want {
		t.Fatalf("beta Chat calls = %d, want %d", got, want)
	}
	if got, want := reg.defaultProvider.callCount(), 0; got != want {
		t.Fatalf("default provider Chat calls = %d, want %d (overrides must bypass default)", got, want)
	}
	// Requests carried the pinned model names.
	if got := reg.named["alpha"].reqs[0].Model; got != "m1" {
		t.Fatalf("alpha request Model = %q, want m1", got)
	}
	if got := reg.named["beta"].reqs[0].Model; got != "m2" {
		t.Fatalf("beta request Model = %q, want m2", got)
	}
	// Child summaries came from the overridden provider, not the default one.
	if got, want := strings.TrimSpace(results[0].Summary), "served by alpha model m1"; got != want {
		t.Fatalf("alpha summary = %q, want %q", got, want)
	}
	if got, want := strings.TrimSpace(results[1].Summary), "served by beta model m2"; got != want {
		t.Fatalf("beta summary = %q, want %q", got, want)
	}

	// Override recorded in branch metadata for observability.
	childSession, err := storeImpl.GetSession(ctx, results[0].ChildSessionID)
	if err != nil {
		t.Fatalf("get child session: %v", err)
	}
	if childSession.Metadata["subagent_provider"] != "alpha" {
		t.Errorf("subagent_provider metadata = %v, want alpha", childSession.Metadata["subagent_provider"])
	}
	if childSession.Metadata["subagent_model"] != "m1" {
		t.Errorf("subagent_model metadata = %v, want m1", childSession.Metadata["subagent_model"])
	}
}

func TestSpawnChild_ModelOnlyOverrideKeepsDefaultProvider(t *testing.T) {
	ctx := context.Background()
	ag, _, reg := newTwoProviderEnv(t)

	res, err := ag.SpawnChild(ctx, "parent-1", ChildSpec{Task: "model-only", Model: "m2"})
	if err != nil {
		t.Fatalf("SpawnChild: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	if got, want := reg.defaultProvider.callCount(), 1; got != want {
		t.Fatalf("default provider Chat calls = %d, want %d", got, want)
	}
	if got, want := reg.defaultProvider.reqs[0].Model, "m2"; got != want {
		t.Fatalf("request Model = %q, want %q (model override applied on default provider)", got, want)
	}
}

func TestSpawnChild_UnknownProviderFailsCleanly(t *testing.T) {
	ctx := context.Background()
	ag, storeImpl, _ := newTwoProviderEnv(t)

	// Count sessions before: a failed spawn must not leave an orphan branch.
	sessionsBefore := len(storeImpl.sessions)

	res, err := ag.SpawnChild(ctx, "parent-1", ChildSpec{Task: "ghost task", Provider: "ghost", Model: "m9"})
	if err == nil {
		t.Fatalf("expected unknown provider error, got child result %+v", res)
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error %q must mention the unknown provider", err.Error())
	}
	if len(storeImpl.sessions) != sessionsBefore {
		t.Fatalf("failed spawn must not branch a session; sessions grew from %d to %d", sessionsBefore, len(storeImpl.sessions))
	}
}

func TestSpawnChild_UnknownProviderViaToolYieldsERRORResult(t *testing.T) {
	// The spawn_subagent path surfaces the same validation to the model as
	// an ERROR tool result (never a panic), per RF-9.3.
	ctx := context.Background()
	ag, _, _ := newTwoProviderEnv(t)
	_, err := ag.SpawnChild(ctx, "parent-1", ChildSpec{Task: "x", Provider: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want unknown provider error, got %v", err)
	}
}
