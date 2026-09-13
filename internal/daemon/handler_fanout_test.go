package daemon

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// fanoutProvider records which model names it was called with and the
// observed concurrency ceiling (upper-bound check only — no wall clocks).
type fanoutProvider struct {
	name      string
	mu        sync.Mutex
	models    []string
	active    int32
	maxActive int32
}

func (p *fanoutProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	cur := atomic.AddInt32(&p.active, 1)
	for {
		maxCur := atomic.LoadInt32(&p.maxActive)
		if cur <= maxCur || atomic.CompareAndSwapInt32(&p.maxActive, maxCur, cur) {
			break
		}
	}
	defer atomic.AddInt32(&p.active, -1)
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	content := "served by " + p.name + " model " + req.Model
	return llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: content}}},
		Usage:   &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func (p *fanoutProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, llm.ErrStreamingNotSupported
}

func (p *fanoutProvider) ListModels() ([]string, error) { return []string{"m1", "m2", "m3"}, nil }
func (p *fanoutProvider) Close() error                  { return nil }

func (p *fanoutProvider) seenModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]string(nil), p.models...)
	return out
}

func (p *fanoutProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.models)
}

// fanoutRegistry exposes two named providers (alpha, beta) plus a default
// one, with provider-name resolution like llm.Registry. It satisfies both
// daemon.LLMRegistryInterface and agent.LLMRegistryInterface.
type fanoutRegistry struct {
	defaultProvider *fanoutProvider
	named           map[string]*fanoutProvider
}

func (r *fanoutRegistry) GetDefault() (llm.Provider, string) {
	return r.defaultProvider, "default-model"
}

func (r *fanoutRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.defaultProvider.Chat(ctx, req)
}

func (r *fanoutRegistry) GetProvider(name string) (llm.Provider, bool) {
	p, ok := r.named[name]
	if !ok {
		return nil, false
	}
	return p, true
}

func (r *fanoutRegistry) Close() error { return nil }

func newFanoutHandler(t *testing.T) (*SessionManager, *Handler, *fanoutRegistry) {
	t.Helper()
	st := newRealStore(t)
	reg := &fanoutRegistry{
		defaultProvider: &fanoutProvider{name: "default"},
		named: map[string]*fanoutProvider{
			"alpha": {name: "alpha"},
			"beta":  {name: "beta"},
		},
	}
	logger := slog.New(slog.DiscardHandler)
	tmp := t.TempDir()
	policy := perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{}},
		Git:   perms.GitPermissions{Allow: []string{}},
	}
	eng, err := perms.New(policy, tmp, logger)
	if err != nil {
		t.Fatalf("perms: %v", err)
	}
	toolsReg := tools.NewDefaultRegistry(eng, tmp, logger)
	cfg := config.Defaults()
	mgr := NewSessionManager(st, reg, toolsReg, NewEmergencyState(logger), logger, cfg, &testPermsEngine{}, st)
	if mgr.agent == nil {
		t.Fatal("agent not initialized: fanout test needs full wiring")
	}
	return mgr, NewHandler(mgr, logger, nil, nil), reg
}

// TestHandlerFanoutRunsModelChildren asserts: entry parsing ("provider/model"
// vs bare model), per-child Chat routing, branch lineage metadata, pool
// boundedness, and compatibility with session.compare for post-fanout
// inspection.
func TestHandlerFanoutRunsModelChildren(t *testing.T) {
	if testing.Short() {
		t.Skip("fanout sqlite turn skipped in -short")
	}
	mgr, handler, reg := newFanoutHandler(t)
	ctx := context.Background()

	params := FanoutParams{
		Task:   "what does this repo do?",
		Models: []string{"alpha/m1", "beta/m2"},
	}
	resp := handler.HandleRequest(ctx, makeRequest(MethodFanout, params))
	if resp.Error != nil {
		t.Fatalf("fanout error: %+v", resp.Error)
	}
	var res FanoutResult
	mustUnmarshal(t, resp.Result, &res)
	if res.ParentSessionID == "" {
		t.Fatal("expected a parent session id")
	}
	if len(res.Children) != 2 {
		t.Fatalf("want 2 children, got %d", len(res.Children))
	}
	// Deterministic order: results match input order like SpawnChildren.
	if res.Children[0].Provider != "alpha" || res.Children[0].Model != "m1" {
		t.Fatalf("child 0 resolved to %s/%s, want alpha/m1", res.Children[0].Provider, res.Children[0].Model)
	}
	if res.Children[1].Provider != "beta" || res.Children[1].Model != "m2" {
		t.Fatalf("child 1 resolved to %s/%s, want beta/m2", res.Children[1].Provider, res.Children[1].Model)
	}

	childIDs := map[string]bool{}
	for i, c := range res.Children {
		if !c.Success {
			t.Fatalf("child %d failed: %s", i, c.Error)
		}
		if c.ChildSessionID == "" || c.ChildSessionID == res.ParentSessionID || childIDs[c.ChildSessionID] {
			t.Fatalf("child %d session id invalid: %q", i, c.ChildSessionID)
		}
		childIDs[c.ChildSessionID] = true
		if c.BranchParent != res.ParentSessionID {
			t.Fatalf("child %d lineage branch_parent = %q, want %q (branched children)", i, c.BranchParent, res.ParentSessionID)
		}
		if want := "served by " + c.Provider + " model " + c.Model; c.Summary != want {
			t.Fatalf("child %d summary = %q, want %q", i, c.Summary, want)
		}
	}

	// Each named provider saw exactly its pinned model name.
	if got := reg.named["alpha"].seenModels(); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("alpha served models %v, want [m1]", got)
	}
	if got := reg.named["beta"].seenModels(); len(got) != 1 || got[0] != "m2" {
		t.Fatalf("beta served models %v, want [m2]", got)
	}
	if got := reg.defaultProvider.seenModels(); len(got) != 0 {
		t.Fatalf("default provider must not be used when overrides are named, served %v", got)
	}

	// session.compare must work parent vs branched child: the divergent tail
	// of the child is exactly its own user+assistant turn.
	cmp := handler.HandleRequest(ctx, makeRequest(MethodCompareSessions, CompareSessionsParams{
		SessionA: res.ParentSessionID,
		SessionB: res.Children[0].ChildSessionID,
	}))
	if cmp.Error != nil {
		t.Fatalf("compare error: %+v", cmp.Error)
	}
	var cmpRes CompareSessionsResult
	mustUnmarshal(t, cmp.Result, &cmpRes)
	if cmpRes.SameSession {
		t.Fatal("parent and branched child must not be detected as the same session")
	}
	if len(cmpRes.DivergentA) != 0 {
		t.Fatalf("parent has no turn, divergent_a must be empty: %+v", cmpRes.DivergentA)
	}
	if len(cmpRes.DivergentB) != 2 {
		t.Fatalf("child divergent tail should be its user+assistant turn (2 msgs), got %+v", cmpRes.DivergentB)
	}

	// Fresh parent must have been created and labeled for fanout.
	parent, ok := mgr.GetSession(ctx, res.ParentSessionID)
	if !ok || parent.Metadata["fanout"] != true {
		t.Fatalf("fresh parent must be created and labeled: %+v", parent)
	}
}

// TestHandlerFanoutBareModelsUseDefaultProviderAndStayBounded: bare model
// entries keep the default provider; 3 children run through the existing
// bounded SpawnChildren pool (limit 2) — the deterministic assertion is the
// upper bound on observed concurrency, never wall-clock timing.
func TestHandlerFanoutBareModelsUseDefaultProviderAndStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("fanout sqlite turn skipped in -short")
	}
	_, handler, reg := newFanoutHandler(t)
	ctx := context.Background()

	resp := handler.HandleRequest(ctx, makeRequest(MethodFanout, FanoutParams{
		Task:   "bounded task",
		Models: []string{"m1", "m2", "m3"},
	}))
	if resp.Error != nil {
		t.Fatalf("fanout error: %+v", resp.Error)
	}
	var res FanoutResult
	mustUnmarshal(t, resp.Result, &res)
	if len(res.Children) != 3 {
		t.Fatalf("want 3 children, got %d", len(res.Children))
	}
	for i, c := range res.Children {
		if !c.Success {
			t.Fatalf("child %d failed: %s", i, c.Error)
		}
		if c.Provider != "" {
			t.Fatalf("bare model entry must use the default provider, got %q", c.Provider)
		}
	}
	if got := reg.defaultProvider.seenModels(); len(got) != 3 {
		t.Fatalf("default provider served %d chats, want 3 (one per bare-model child)", len(got))
	}
	upper := config.DefaultAgentMaxParallelChildren
	if int(reg.defaultProvider.maxActive) > upper {
		t.Fatalf("observed concurrency %d exceeds bounded pool limit %d", reg.defaultProvider.maxActive, upper)
	}
}

// TestHandlerFanoutValidation covers param-level rejections: these must be
// JSON-RPC errors, and the parent session must keep its existing transcript
// when the parent id is unknown (no silent fresh-parent fallback).
func TestHandlerFanoutValidation(t *testing.T) {
	_, handler, _ := newFanoutHandler(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		params   FanoutParams
		wantCode int
	}{
		{
			name:     "empty task",
			params:   FanoutParams{Models: []string{"m1"}},
			wantCode: ErrCodeInvalidParams,
		},
		{
			name:     "no models",
			params:   FanoutParams{Task: "task"},
			wantCode: ErrCodeInvalidParams,
		},
		{
			name:     "unknown parent session",
			params:   FanoutParams{Task: "task", Models: []string{"m1"}, SessionID: "does-not-exist"},
			wantCode: ErrCodeSessionNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := handler.HandleRequest(ctx, makeRequest(MethodFanout, tc.params))
			if resp.Error == nil || resp.Error.Code != tc.wantCode {
				t.Fatalf("want code %d, got %+v", tc.wantCode, resp.Error)
			}
		})
	}
}
