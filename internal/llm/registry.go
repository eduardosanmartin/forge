// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter and a model registry supporting hot-swap.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/routing"
)

// ModelInfo describes a model available in the registry.
type ModelInfo struct {
	Name     string
	Provider string
	Kind     string
}

// ProviderInfo describes one configured provider (name + kind only — not its
// model catalog, which is looked up separately via ListProviderModels since
// it means a live network call).
type ProviderInfo struct {
	Name string
	Kind string
}

// modelRefresher is implemented by every provider constructor in this
// package; type-asserted (like requestTimeoutSetter) rather than added to
// the shared Provider interface, so a provider kind without a refreshable
// catalog still satisfies Provider.
type modelRefresher interface {
	RefreshModels() error
}

// fallbackEntry is one parsed "provider/model" step of config.FallbackChain.
type fallbackEntry struct {
	provider string
	model    string
}

// defaultCooldown is how long a provider/model stays skipped by
// ResolveAttempts after a retryable failure when the failure carried no
// Retry-After header. Deliberately short: the point is to stop hammering a
// model that just failed on every single turn, not to lock it out for an
// operator-visible amount of time — 30s covers a typical rate-limit blip
// without meaningfully delaying recovery once the provider is healthy again.
const defaultCooldown = 30 * time.Second

// Registry manages multiple LLM providers and supports hot-swapping the default model.
type Registry struct {
	providers       map[string]Provider
	providerKinds   map[string]string
	defaultProvider string
	defaultModel    string
	router          *routing.ModelRouter
	allowedHosts    []string
	logger          *slog.Logger
	mu              sync.RWMutex
	// fallbackChain backs ChatWithFallback (RNF: failover on rate limit/
	// transient outage). Parsed once at construction from
	// config.Config.FallbackChain — malformed or unknown-provider entries
	// are dropped with a warning rather than failing the whole daemon:
	// Config.Validate() is the strict gate; a config that somehow reached
	// here without validation degrades gracefully instead of panicking.
	fallbackChain []fallbackEntry
	// cooldowns holds "provider/model" -> until, for entries ResolveAttempts
	// should skip because they failed retryably too recently (see
	// RecordFailure). Read/written under mu like everything else here;
	// lazily checked on each call, never swept by a background timer, so an
	// entry that's expired just stops affecting anything the next time
	// ResolveAttempts reads it.
	cooldowns map[string]time.Time
	// clock is time.Now by default; overridable in tests so cooldown
	// expiry can be exercised deterministically without a real sleep.
	clock func() time.Time
}

// New creates a new Registry from configuration.
// It builds one Provider per config.Providers entry, validating the allowlist per provider.
func New(cfg *config.Config, allowedHosts []string, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	r := &Registry{
		providers:       make(map[string]Provider),
		providerKinds:   make(map[string]string),
		defaultProvider: cfg.DefaultProvider,
		defaultModel:    "",
		allowedHosts:    allowedHosts,
		logger:          logger,
		cooldowns:       make(map[string]time.Time),
		clock:           time.Now,
	}

	// Build providers
	for name, p := range cfg.Providers {
		provider, err := r.createProvider(name, p)
		if err != nil {
			// Clean up any already created providers
			for _, prov := range r.providers {
				_ = prov.Close()
			}
			return nil, fmt.Errorf("create provider %q: %w", name, err)
		}
		r.providers[name] = provider
		r.providerKinds[name] = p.Kind
	}

	// Validate default provider exists
	if _, ok := r.providers[r.defaultProvider]; !ok {
		for _, prov := range r.providers {
			_ = prov.Close()
		}
		return nil, fmt.Errorf("default_provider %q not found in providers", r.defaultProvider)
	}

	// Build model router from config
	r.router = r.buildModelRouter(cfg)

	// Parse the fallback chain (opt-in — empty/absent disables ChatWithFallback
	// entirely, see that method). Entries are "provider/model"; a malformed
	// entry or one naming a provider not in this registry is dropped with a
	// warning instead of failing construction.
	for i, entry := range cfg.FallbackChain {
		providerName, model, ok := strings.Cut(entry, "/")
		if !ok || providerName == "" || model == "" {
			logger.Warn("fallback_chain entry malformed, skipping", "index", i, "entry", entry)
			continue
		}
		if _, exists := r.providers[providerName]; !exists {
			logger.Warn("fallback_chain entry names an unconfigured provider, skipping", "index", i, "entry", entry)
			continue
		}
		r.fallbackChain = append(r.fallbackChain, fallbackEntry{provider: providerName, model: model})
	}

	// Set default model: config-declared models take priority, provider list as fallback
	if provider, ok := r.providers[r.defaultProvider]; ok {
		if len(cfg.Providers[r.defaultProvider].Models) > 0 {
			r.defaultModel = cfg.Providers[r.defaultProvider].Models[0]
		} else {
			models, err := provider.ListModels()
			if err == nil && len(models) > 0 {
				r.defaultModel = models[0]
			}
		}
	}

	return r, nil
}

func (r *Registry) buildModelRouter(cfg *config.Config) *routing.ModelRouter {
	roleModels := make(map[routing.ModelRole]string)

	// Collect model roles from all providers
	for _, p := range cfg.Providers {
		for role, model := range p.ModelRoles {
			switch role {
			case "cheap":
				roleModels[routing.RoleCheap] = model
			case "generation":
				roleModels[routing.RoleGeneration] = model
			case "reasoning":
				roleModels[routing.RoleReasoning] = model
			}
		}
	}

	// Fallback to first model in default provider if no roles configured
	if len(roleModels) == 0 {
		if len(cfg.Providers[r.defaultProvider].Models) > 0 {
			roleModels[routing.RoleGeneration] = cfg.Providers[r.defaultProvider].Models[0]
		}
	}

	return routing.NewModelRouter(roleModels)
}

// requestTimeoutSetter is implemented by every provider constructor below;
// it's a local optional interface (rather than adding SetRequestTimeout to
// the shared Provider interface) so provider kinds added later without
// per-request timeout support still satisfy Provider.
type requestTimeoutSetter interface {
	SetRequestTimeout(time.Duration)
}

func (r *Registry) createProvider(name string, p config.Provider) (Provider, error) {
	var (
		provider Provider
		err      error
	)
	switch p.Kind {
	case "openai-compatible":
		provider, err = NewOpenAICompatibleProvider(p.BaseURL, p.APIKey, r.allowedHosts, r.logger)
	case "anthropic":
		provider, err = NewAnthropicProvider(p.BaseURL, p.APIKey, r.allowedHosts, r.logger)
	case "gemini":
		provider, err = NewGeminiProvider(p.BaseURL, p.APIKey, r.allowedHosts, r.logger)
	default:
		return nil, fmt.Errorf("unknown provider kind %q", p.Kind)
	}
	if err != nil {
		return nil, err
	}
	// config.DefaultRequestTimeoutSeconds (== the constructors' own built-in
	// default) makes this a no-op when unset; only an explicit override
	// changes anything.
	if p.RequestTimeoutSeconds > 0 {
		if setter, ok := provider.(requestTimeoutSetter); ok {
			setter.SetRequestTimeout(time.Duration(p.RequestTimeoutSeconds) * time.Second)
		}
	}
	return provider, nil
}

// GetProvider returns a provider by name.
func (r *Registry) GetProvider(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// GetDefault returns the current default provider and model name.
// Returns (nil, "") if the registry has been closed.
func (r *Registry) GetDefault() (Provider, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.providers == nil {
		return nil, ""
	}
	return r.providers[r.defaultProvider], r.defaultModel
}

// GetModelForStep returns the model name for a given step type.
// Uses the router to select the appropriate model based on step type.
func (r *Registry) GetModelForStep(step routing.StepType) string {
	if r.router != nil {
		return r.router.ModelForStep(step)
	}
	// Fallback to default model
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultModel
}

// GetRouter returns the model router (for testing/inspection).
func (r *Registry) GetRouter() *routing.ModelRouter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.router
}

// SetDefault changes the default model (hot-swap).
// Validates that the model exists in the current default provider's ListModels().
func (r *Registry) SetDefault(model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	provider, ok := r.providers[r.defaultProvider]
	if !ok {
		return fmt.Errorf("default provider %q not found", r.defaultProvider)
	}

	models, err := provider.ListModels()
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}

	found := false
	for _, m := range models {
		if m == model {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("model %q not available in provider %q", model, r.defaultProvider)
	}

	r.defaultModel = model
	r.logger.Info("default model changed", "model", model, "provider", r.defaultProvider)
	return nil
}

// ListProviders returns every configured provider's name and kind, sorted by
// name. Instant (no network call) — it reads the registry's own
// construction-time config, not a live catalog; see ListProviderModels for
// that.
func (r *Registry) ListProviders() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderInfo, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, ProviderInfo{Name: name, Kind: r.providerKinds[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListProviderModels returns the LIVE model catalog for one named provider —
// forces a fresh RefreshModels() call first (when the provider supports it)
// rather than serving whatever was cached at daemon startup, so a model
// added to the provider after the daemon started still shows up. This is
// what lets a provider switch offer every model the provider actually has,
// not just the ones declared in providers.<name>.models.
func (r *Registry) ListProviderModels(name string) ([]string, error) {
	r.mu.RLock()
	provider, ok := r.providers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	if refresher, ok := provider.(modelRefresher); ok {
		if err := refresher.RefreshModels(); err != nil {
			return nil, fmt.Errorf("refresh models for provider %q: %w", name, err)
		}
	}
	return provider.ListModels()
}

// SwitchProviderAndModel atomically switches BOTH the default provider and
// model (hot-swap, like SetDefault) — for an explicit "provider/model"
// selection rather than a bare model name search within the current
// provider. Validates model against the target provider's freshly refreshed
// live catalog (ListProviderModels), so an undeclared-but-real model works
// here exactly like a declared one.
func (r *Registry) SwitchProviderAndModel(providerName, model string) error {
	models, err := r.ListProviderModels(providerName)
	if err != nil {
		return err
	}
	found := false
	for _, m := range models {
		if m == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q not available in provider %q", model, providerName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultProvider = providerName
	r.defaultModel = model
	r.logger.Info("default provider and model changed", "provider", providerName, "model", model)
	return nil
}

// ListAll returns all models from all providers with provider tags.
func (r *Registry) ListAll() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ModelInfo
	for name, provider := range r.providers {
		models, err := provider.ListModels()
		if err != nil {
			r.logger.Warn("list models failed", "provider", name, "error", err)
			continue
		}
		for _, m := range models {
			kind := r.providerKinds[name]
			if kind == "" {
				kind = "openai-compatible"
			}
			result = append(result, ModelInfo{
				Name:     m,
				Provider: name,
				Kind:     kind,
			})
		}
	}
	return result
}

// RegisterProvider registers an external LLM provider (e.g., a provider-plugin) under name.
// It is additive and safe to call after New. If a provider with the same name already
// exists, it is replaced (old provider is closed). This is the hook for daemon's
// provider-plugin wiring (WU2): plugins with kind=provider register as providers
// named by manifest, and daemon config may select one as the default model source.
// The provider is not persisted across restarts; callers must re-register after reload.
func (r *Registry) RegisterProvider(name string, p Provider) error {
	if name == "" {
		return fmt.Errorf("provider name must not be empty")
	}
	if p == nil {
		return fmt.Errorf("provider must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	if old, ok := r.providers[name]; ok {
		_ = old.Close()
	}
	r.providers[name] = p
	if r.providerKinds == nil {
		r.providerKinds = make(map[string]string)
	}
	r.providerKinds[name] = "plugin-provider"
	return nil
}

// Close closes all providers.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for name, provider := range r.providers {
		if err := provider.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close provider %q: %w", name, err))
		}
	}
	r.providers = nil
	return errors.Join(errs...)
}

// FallbackTarget is one resolved candidate a failover-aware call may try —
// the live provider instance plus its name and model. Exposed (via
// ResolveAttempts) for callers that must walk candidates themselves rather
// than through ChatWithFallback: the streaming path
// (agent.Agent.callLLMStreamWithFailover) can only tell a safe-to-retry
// failure (nothing streamed to the caller yet) from a mid-stream one by
// consuming the channel itself, something Registry has no part in, so it
// hands over the resolved candidates and lets the caller report outcomes
// back via RecordSuccess/RecordFailure.
type FallbackTarget struct {
	Provider     Provider
	ProviderName string
	Model        string
}

func attemptKey(providerName, model string) string {
	return providerName + "/" + model
}

// ResolveAttempts returns the ordered candidates a failover-aware call
// should try: the current default first, then each fallback_chain entry in
// its configured order (skipping any whose provider is no longer
// registered). An entry currently in cooldown from a recent retryable
// failure (see RecordFailure) is skipped — UNLESS every candidate is
// cooling, in which case cooldowns are ignored for this call entirely: a
// single wrong or over-long Retry-After must never wedge every model, so
// the fallback degrades to "try in order" rather than "have nothing left to
// try." The default is whatever RecordSuccess last promoted (sticky), not
// necessarily config.DefaultProvider — a successful fallback makes the
// model that worked the new default for subsequent calls.
func (r *Registry) ResolveAttempts() []FallbackTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool, 1+len(r.fallbackChain))
	all := make([]FallbackTarget, 0, 1+len(r.fallbackChain))
	if p, ok := r.providers[r.defaultProvider]; ok && p != nil {
		all = append(all, FallbackTarget{Provider: p, ProviderName: r.defaultProvider, Model: r.defaultModel})
		seen[attemptKey(r.defaultProvider, r.defaultModel)] = true
	}
	for _, e := range r.fallbackChain {
		// A sticky promotion (RecordSuccess) can turn a chain entry into the
		// current default; skip it here rather than list it twice — the
		// entry above already covers it.
		key := attemptKey(e.provider, e.model)
		if seen[key] {
			continue
		}
		if p, ok := r.providers[e.provider]; ok {
			all = append(all, FallbackTarget{Provider: p, ProviderName: e.provider, Model: e.model})
			seen[key] = true
		}
	}

	now := r.clock()
	fresh := make([]FallbackTarget, 0, len(all))
	for _, t := range all {
		until, cooling := r.cooldowns[attemptKey(t.ProviderName, t.Model)]
		if !cooling || !now.Before(until) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) == 0 {
		return all
	}
	return fresh
}

// RecordSuccess marks providerName/model as the sticky default: later calls
// go straight to it instead of re-trying whatever failed first. A no-op
// beyond clearing its cooldown when it's already the default. Called after
// every successful attempt in ChatWithFallback/callLLMStreamWithFailover,
// including the very first one, so a plain, always-succeeding default just
// keeps re-confirming itself at negligible cost (a map delete and two
// string comparisons under the existing lock).
func (r *Registry) RecordSuccess(providerName, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cooldowns, attemptKey(providerName, model))
	if r.defaultProvider == providerName && r.defaultModel == model {
		return
	}
	r.defaultProvider = providerName
	r.defaultModel = model
	r.logger.Warn("fallback promoted to sticky default", "provider", providerName, "model", model)
}

// RecordFailure puts providerName/model into cooldown after a retryable
// failure, so ResolveAttempts skips it for a while instead of offering it
// again on every subsequent call. Uses the failure's Retry-After when the
// provider sent one (RetryAfterOf), otherwise defaultCooldown. Callers must
// only call this for a failure that IsRetryable — a non-retryable one
// (bad request, auth) isn't transient, so cooling down wouldn't help and
// would just needlessly hide the model from ResolveAttempts.
func (r *Registry) RecordFailure(providerName, model string, err error) {
	d := RetryAfterOf(err)
	if d <= 0 {
		d = defaultCooldown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldowns[attemptKey(providerName, model)] = r.clock().Add(d)
}

// ChatWithFallback sends req against ResolveAttempts' first candidate
// (normally the default, sticky-adjusted); on a RetryableError (rate limit,
// transient upstream outage, network timeout — see IsRetryable) it walks
// the remaining candidates in order, one attempt per entry, until one
// succeeds or they run out. A non-retryable failure — from the first
// candidate OR from any later one — stops immediately without trying
// what's left: swapping models can't fix a malformed request or an auth
// failure, so continuing would just burn attempts on a copy of the same
// broken call. A successful attempt is recorded via RecordSuccess (sticky
// promotion); a retryable failure via RecordFailure (cooldown).
//
// With no fallback_chain configured (the default — this method is opt-in
// exactly like the config field), a single failure returns immediately,
// identical to calling Chat.
//
// On exhaustion, the returned error joins every attempt (via errors.Join)
// so the daemon log shows the whole chain that was tried, not just the last
// failure.
func (r *Registry) ChatWithFallback(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	attempts := r.ResolveAttempts()
	if len(attempts) == 0 {
		return ChatResponse{}, errors.New("no default provider available")
	}

	first := attempts[0]
	firstReq := req
	firstReq.Model = first.Model
	resp, err := first.Provider.Chat(ctx, firstReq)
	if err == nil {
		r.RecordSuccess(first.ProviderName, first.Model)
		return resp, nil
	}
	if !IsRetryable(err) || len(attempts) == 1 {
		return resp, err
	}
	r.RecordFailure(first.ProviderName, first.Model, err)

	errs := []error{fmt.Errorf("%s/%s: %w", first.ProviderName, first.Model, err)}
	for _, target := range attempts[1:] {
		fbReq := req
		fbReq.Model = target.Model
		fbResp, fbErr := target.Provider.Chat(ctx, fbReq)
		if fbErr == nil {
			r.RecordSuccess(target.ProviderName, target.Model)
			r.logger.Warn("fell back to next model in fallback_chain",
				"from_provider", first.ProviderName, "from_model", first.Model,
				"to_provider", target.ProviderName, "to_model", target.Model)
			return fbResp, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s: %w", target.ProviderName, target.Model, fbErr))
		if !IsRetryable(fbErr) {
			break
		}
		r.RecordFailure(target.ProviderName, target.Model, fbErr)
	}
	return ChatResponse{}, fmt.Errorf("fallback_chain exhausted: %w", errors.Join(errs...))
}

// Chat sends a chat request using the default provider and model.
func (r *Registry) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	r.mu.RLock()
	provider := r.providers[r.defaultProvider]
	model := r.defaultModel
	r.mu.RUnlock()

	if provider == nil {
		return ChatResponse{}, errors.New("no default provider available")
	}

	if req.Model == "" {
		req.Model = model
	}

	return provider.Chat(ctx, req)
}

// ChatForStep sends a chat request using the model selected for the given step type.
func (r *Registry) ChatForStep(ctx context.Context, step routing.StepType, req ChatRequest) (ChatResponse, error) {
	r.mu.RLock()
	provider := r.providers[r.defaultProvider]
	r.mu.RUnlock()

	if provider == nil {
		return ChatResponse{}, errors.New("no default provider available")
	}

	model := r.GetModelForStep(step)
	if req.Model == "" {
		req.Model = model
	}

	return provider.Chat(ctx, req)
}

// ChatStream sends a streaming chat request using the default provider and model.
func (r *Registry) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	r.mu.RLock()
	provider := r.providers[r.defaultProvider]
	model := r.defaultModel
	r.mu.RUnlock()

	if provider == nil {
		return nil, errors.New("no default provider available")
	}

	if req.Model == "" {
		req.Model = model
	}

	return provider.ChatStream(ctx, req)
}

// ChatStreamForStep sends a streaming chat request using the model for the given step type.
func (r *Registry) ChatStreamForStep(ctx context.Context, step routing.StepType, req ChatRequest) (<-chan StreamChunk, error) {
	r.mu.RLock()
	provider := r.providers[r.defaultProvider]
	r.mu.RUnlock()

	if provider == nil {
		return nil, errors.New("no default provider available")
	}

	model := r.GetModelForStep(step)
	if req.Model == "" {
		req.Model = model
	}

	return provider.ChatStream(ctx, req)
}
