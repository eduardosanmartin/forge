// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/routing"
)

// ModelInfo describes a model available in the registry.
type ModelInfo struct {
	Name     string
	Provider string
	Kind     string
}

// Registry manages multiple LLM providers and supports hot-swapping the default model.
type Registry struct {
	providers       map[string]Provider
	defaultProvider string
	defaultModel    string
	router          *routing.ModelRouter
	allowedHosts    []string
	logger          *slog.Logger
	mu              sync.RWMutex
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
		defaultProvider: cfg.DefaultProvider,
		defaultModel:    "",
		allowedHosts:    allowedHosts,
		logger:          logger,
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

	// Set default model: config-declared models take priority, Ollama list as fallback
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

func (r *Registry) createProvider(name string, p config.Provider) (Provider, error) {
	switch p.Kind {
	case "openai-compatible":
		return NewOllamaProvider(p.BaseURL, p.APIKey, r.allowedHosts, r.logger)
	default:
		return nil, fmt.Errorf("unknown provider kind %q", p.Kind)
	}
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
			result = append(result, ModelInfo{
				Name:     m,
				Provider: name,
				Kind:     "openai-compatible",
			})
		}
	}
	return result
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
