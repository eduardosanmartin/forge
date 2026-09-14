package cost

import (
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
)

func msg(prompt, completion, total int) store.Message {
	return store.Message{Usage: &llm.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}}
}

func TestEstimateSessionCostNoUsage(t *testing.T) {
	sc := EstimateSessionCost("s1", nil, nil, nil, "openrouter")
	if sc.TotalTokens != 0 || sc.Priced || sc.EstimatedUSD != 0 {
		t.Errorf("expected zero/unpriced, got %+v", sc)
	}
	if sc.SessionID != "s1" || sc.Provider != "openrouter" {
		t.Errorf("session_id/provider not set correctly: %+v", sc)
	}
}

func TestEstimateSessionCostSumsUsageIgnoringNil(t *testing.T) {
	messages := []store.Message{
		msg(100, 50, 150),
		{}, // no Usage (e.g. a user message) — must not panic or count
		msg(200, 25, 225),
	}
	sc := EstimateSessionCost("s1", nil, messages, nil, "openrouter")
	if sc.PromptTokens != 300 || sc.CompletionTokens != 75 || sc.TotalTokens != 375 {
		t.Errorf("sums wrong: %+v", sc)
	}
}

func TestEstimateSessionCostUnpricedProviderStaysUnpriced(t *testing.T) {
	messages := []store.Message{msg(1_000_000, 1_000_000, 2_000_000)}
	providers := map[string]config.Provider{
		"ollama": {}, // zero pricing: local/free
	}
	sc := EstimateSessionCost("s1", nil, messages, providers, "ollama")
	if sc.Priced {
		t.Errorf("expected an unpriced provider to stay unpriced, got %+v", sc)
	}
	if sc.EstimatedUSD != 0 {
		t.Errorf("unpriced provider must not report a nonzero cost, got %v", sc.EstimatedUSD)
	}
}

func TestEstimateSessionCostPricedProviderComputesUSD(t *testing.T) {
	messages := []store.Message{msg(1_000_000, 500_000, 1_500_000)}
	providers := map[string]config.Provider{
		"openrouter": {PricePerMillionInputTokens: 3.0, PricePerMillionOutputTokens: 15.0},
	}
	sc := EstimateSessionCost("s1", nil, messages, providers, "openrouter")
	if !sc.Priced {
		t.Fatal("expected priced=true")
	}
	want := 1.0*3.0 + 0.5*15.0 // 1M input @ $3/M + 0.5M output @ $15/M = $10.50
	if diff := sc.EstimatedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("EstimatedUSD = %v, want %v", sc.EstimatedUSD, want)
	}
}

func TestEstimateSessionCostReadsModelFromMetadata(t *testing.T) {
	sc := EstimateSessionCost("s1", map[string]any{"model": "gpt-5"}, nil, nil, "openrouter")
	if sc.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", sc.Model)
	}
	scNoModel := EstimateSessionCost("s1", nil, nil, nil, "openrouter")
	if scNoModel.Model != "" {
		t.Errorf("Model = %q, want empty when metadata has no model", scNoModel.Model)
	}
}

func TestAggregateByProviderSumsAcrossSessions(t *testing.T) {
	providers := map[string]config.Provider{
		"openrouter": {PricePerMillionInputTokens: 3.0, PricePerMillionOutputTokens: 15.0},
	}
	costs := []SessionCost{
		EstimateSessionCost("a", nil, []store.Message{msg(1_000_000, 0, 1_000_000)}, providers, "openrouter"),
		EstimateSessionCost("b", nil, []store.Message{msg(1_000_000, 0, 1_000_000)}, providers, "openrouter"),
	}
	agg := AggregateByProvider(costs)
	if len(agg) != 1 {
		t.Fatalf("expected 1 provider group, got %d", len(agg))
	}
	pc := agg[0]
	if pc.Sessions != 2 || pc.PromptTokens != 2_000_000 {
		t.Errorf("aggregate wrong: %+v", pc)
	}
	if !pc.Priced {
		t.Fatal("expected priced=true when every session is priced")
	}
	want := 6.0 // 2 * $3
	if diff := pc.EstimatedUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("EstimatedUSD = %v, want %v", pc.EstimatedUSD, want)
	}
}

func TestAggregateByProviderMixedPricingIsConservative(t *testing.T) {
	costs := []SessionCost{
		{SessionID: "a", Provider: "openrouter", TotalTokens: 100, Priced: true, EstimatedUSD: 5.0},
		{SessionID: "b", Provider: "openrouter", TotalTokens: 100, Priced: false}, // e.g. pricing removed mid-history
	}
	agg := AggregateByProvider(costs)
	if len(agg) != 1 {
		t.Fatalf("expected 1 provider group, got %d", len(agg))
	}
	if agg[0].Priced {
		t.Error("a provider group with any unpriced session must not report itself as priced")
	}
	if agg[0].EstimatedUSD != 0 {
		t.Errorf("must not report a partial (undercounting) dollar figure, got %v", agg[0].EstimatedUSD)
	}
	if agg[0].Sessions != 2 {
		t.Errorf("sessions = %d, want 2 (both still counted)", agg[0].Sessions)
	}
}

func TestAggregateByProviderPreservesFirstSeenOrder(t *testing.T) {
	costs := []SessionCost{
		{SessionID: "a", Provider: "zen"},
		{SessionID: "b", Provider: "openrouter"},
		{SessionID: "c", Provider: "zen"},
	}
	agg := AggregateByProvider(costs)
	if len(agg) != 2 || agg[0].Provider != "zen" || agg[1].Provider != "openrouter" {
		t.Errorf("order/grouping wrong: %+v", agg)
	}
}
