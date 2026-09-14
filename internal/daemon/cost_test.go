package daemon

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/cost"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
)

func newCostMgr(t *testing.T, st *store.Store, cfg *config.Config) *SessionManager {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return NewSessionManager(st, &branchLLM{}, &branchToolsImpl{}, NewEmergencyState(nil), nil, cfg, nil, st)
}

func appendUsageMessage(t *testing.T, st *store.Store, sessionID, role string, prompt, completion, total int) {
	t.Helper()
	_, _, err := st.AppendMessage(context.Background(), &store.Message{
		SessionID: sessionID, Role: role, Content: "x",
		Usage: &llm.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
}

func TestHandlerSessionCostUnpriced(t *testing.T) {
	st := newRealStore(t)
	cfg := &config.Config{
		DefaultProvider: "ollama",
		Providers:       map[string]config.Provider{"ollama": {}}, // no pricing: local/free
	}
	mgr := newCostMgr(t, st, cfg)
	handler := NewHandler(mgr, nil, nil, nil)

	sess, err := st.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendUsageMessage(t, st, sess.ID, "assistant", 100, 50, 150)

	resp := handler.HandleRequest(context.Background(), makeRequest(MethodSessionCost, SessionCostParams{SessionID: sess.ID}))
	if resp.Error != nil {
		t.Fatalf("session.cost error: %+v", resp.Error)
	}
	var res cost.SessionCost
	mustUnmarshal(t, resp.Result, &res)
	if res.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", res.TotalTokens)
	}
	if res.Priced {
		t.Error("expected an unpriced local provider to report Priced=false")
	}
	if res.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", res.Provider)
	}
}

func TestHandlerSessionCostPriced(t *testing.T) {
	st := newRealStore(t)
	cfg := &config.Config{
		DefaultProvider: "openrouter",
		Providers: map[string]config.Provider{
			"openrouter": {PricePerMillionInputTokens: 3.0, PricePerMillionOutputTokens: 15.0},
		},
	}
	mgr := newCostMgr(t, st, cfg)
	handler := NewHandler(mgr, nil, nil, nil)

	sess, err := st.CreateSession(context.Background(), map[string]any{"model": "gpt-5"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	appendUsageMessage(t, st, sess.ID, "assistant", 1_000_000, 0, 1_000_000)

	resp := handler.HandleRequest(context.Background(), makeRequest(MethodSessionCost, SessionCostParams{SessionID: sess.ID}))
	if resp.Error != nil {
		t.Fatalf("session.cost error: %+v", resp.Error)
	}
	var res cost.SessionCost
	mustUnmarshal(t, resp.Result, &res)
	if !res.Priced || res.EstimatedUSD != 3.0 {
		t.Errorf("expected priced $3.00, got %+v", res)
	}
	if res.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", res.Model)
	}
}

func TestHandlerSessionCostNotFound(t *testing.T) {
	st := newRealStore(t)
	mgr := newCostMgr(t, st, nil)
	handler := NewHandler(mgr, nil, nil, nil)

	resp := handler.HandleRequest(context.Background(), makeRequest(MethodSessionCost, SessionCostParams{SessionID: "nonexistent"}))
	if resp.Error == nil || resp.Error.Code != ErrCodeSessionNotFound {
		t.Fatalf("expected ErrCodeSessionNotFound, got %+v", resp.Error)
	}
}

func TestHandlerCostSummaryAggregatesAcrossSessions(t *testing.T) {
	st := newRealStore(t)
	cfg := &config.Config{
		DefaultProvider: "openrouter",
		Providers: map[string]config.Provider{
			"openrouter": {PricePerMillionInputTokens: 3.0, PricePerMillionOutputTokens: 15.0},
		},
	}
	mgr := newCostMgr(t, st, cfg)
	handler := NewHandler(mgr, nil, nil, nil)

	for i := 0; i < 2; i++ {
		sess, err := st.CreateSession(context.Background(), nil)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		appendUsageMessage(t, st, sess.ID, "assistant", 1_000_000, 0, 1_000_000)
	}

	resp := handler.HandleRequest(context.Background(), makeRequest(MethodCostSummary, CostSummaryParams{}))
	if resp.Error != nil {
		t.Fatalf("cost.summary error: %+v", resp.Error)
	}
	var res CostSummaryResult
	mustUnmarshal(t, resp.Result, &res)
	if len(res.Providers) != 1 {
		t.Fatalf("expected 1 provider group, got %d: %+v", len(res.Providers), res.Providers)
	}
	pc := res.Providers[0]
	if pc.Provider != "openrouter" || pc.Sessions != 2 || pc.EstimatedUSD != 6.0 {
		t.Errorf("aggregate wrong: %+v", pc)
	}
}
