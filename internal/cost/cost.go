// Package cost estimates monetary cost from token usage already recorded by
// forge (RNF-2.1's TurnMetrics/message Usage), for paid providers only
// (RNF-6.3). It never contacts a provider or the network — every figure is
// derived from local token counts and pricing the operator configured.
//
// Scope limitation, stated plainly rather than silently assumed: forge does
// not currently record which provider produced any given message — session
// metadata records at most a "model" string (set by session.switch_model,
// which itself hot-swaps the *daemon's* default provider — see
// internal/daemon/session_mgr.go), never a provider name. So every
// estimate here is attributed to the daemon's configured default provider.
// A session that changed models mid-conversation, or a fanout child that
// ran on a different provider for one task, is not attributed separately —
// "estimado" in the requirement text is exactly the acknowledgment that
// this is an approximation, not a precise ledger.
package cost

import (
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/store"
)

// SessionCost is one session's aggregated token usage and, when the
// attributed provider has pricing configured, its estimated cost. Priced is
// false whenever no price is configured for that provider — meaning "not
// applicable" (e.g. a free local model), never "zero cost".
type SessionCost struct {
	SessionID        string  `json:"session_id"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Priced           bool    `json:"priced"`
	EstimatedUSD     float64 `json:"estimated_usd,omitempty"`
}

// ProviderCost aggregates SessionCost across every session attributed to
// one provider.
type ProviderCost struct {
	Provider         string  `json:"provider"`
	Sessions         int     `json:"sessions"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Priced           bool    `json:"priced"`
	EstimatedUSD     float64 `json:"estimated_usd,omitempty"`
}

// EstimateSessionCost sums Usage across messages (nil Usage entries, e.g.
// user/tool messages, contribute nothing) and, when the resolved provider
// has non-zero pricing configured, estimates cost from that total. model is
// read from sessionMetadata["model"] when present (see the package doc for
// why provider is not similarly resolved per-session).
func EstimateSessionCost(sessionID string, sessionMetadata map[string]any, messages []store.Message, providers map[string]config.Provider, defaultProvider string) SessionCost {
	sc := SessionCost{SessionID: sessionID, Provider: defaultProvider}
	if model, ok := sessionMetadata["model"].(string); ok {
		sc.Model = model
	}

	for _, m := range messages {
		if m.Usage == nil {
			continue
		}
		sc.PromptTokens += m.Usage.PromptTokens
		sc.CompletionTokens += m.Usage.CompletionTokens
		sc.TotalTokens += m.Usage.TotalTokens
	}

	p, ok := providers[defaultProvider]
	if !ok || (p.PricePerMillionInputTokens == 0 && p.PricePerMillionOutputTokens == 0) {
		return sc // not priced: leave Priced false, EstimatedUSD 0
	}
	sc.Priced = true
	sc.EstimatedUSD = float64(sc.PromptTokens)/1_000_000*p.PricePerMillionInputTokens +
		float64(sc.CompletionTokens)/1_000_000*p.PricePerMillionOutputTokens
	return sc
}

// AggregateByProvider sums a set of SessionCost values into one ProviderCost
// per distinct provider. A provider's Priced is true only when EVERY
// session attributed to it was priced — a mix would make the summed
// EstimatedUSD an undercount, which is worse than admitting the aggregate
// isn't fully priced.
func AggregateByProvider(costs []SessionCost) []ProviderCost {
	order := []string{}
	byProvider := map[string]*ProviderCost{}
	anyUnpriced := map[string]bool{}

	for _, c := range costs {
		agg, ok := byProvider[c.Provider]
		if !ok {
			agg = &ProviderCost{Provider: c.Provider, Priced: true}
			byProvider[c.Provider] = agg
			order = append(order, c.Provider)
		}
		agg.Sessions++
		agg.PromptTokens += c.PromptTokens
		agg.CompletionTokens += c.CompletionTokens
		agg.TotalTokens += c.TotalTokens
		if c.Priced {
			agg.EstimatedUSD += c.EstimatedUSD
		} else {
			anyUnpriced[c.Provider] = true
		}
	}

	out := make([]ProviderCost, 0, len(order))
	for _, name := range order {
		agg := byProvider[name]
		if anyUnpriced[name] {
			agg.Priced = false
			agg.EstimatedUSD = 0
		}
		out = append(out, *agg)
	}
	return out
}
