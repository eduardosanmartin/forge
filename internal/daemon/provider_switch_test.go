package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
)

// mockSwitchableRegistry is a small in-memory multi-provider registry
// implementing every optional interface SwitchModel/ListProviders* type-
// assert against (modelSetter, providerSwitcher, allModelsLister,
// providerLister) — no real HTTP involved, catalog is just a map.
type mockSwitchableRegistry struct {
	defaultProvider string
	defaultModel    string
	catalog         map[string][]string // provider -> models
	provider        *modelCapturingProvider
}

func newMockSwitchableRegistry(defaultProvider, defaultModel string, catalog map[string][]string) *mockSwitchableRegistry {
	return &mockSwitchableRegistry{
		defaultProvider: defaultProvider,
		defaultModel:    defaultModel,
		catalog:         catalog,
		provider:        &modelCapturingProvider{},
	}
}

func (r *mockSwitchableRegistry) GetDefault() (llm.Provider, string) { return r.provider, r.defaultModel }
func (r *mockSwitchableRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.provider.Chat(ctx, req)
}
func (r *mockSwitchableRegistry) Close() error { return nil }

func (r *mockSwitchableRegistry) SetDefault(model string) error {
	for _, m := range r.catalog[r.defaultProvider] {
		if m == model {
			r.defaultModel = model
			return nil
		}
	}
	return fmt.Errorf("model %q not available in provider %q", model, r.defaultProvider)
}

func (r *mockSwitchableRegistry) SwitchProviderAndModel(provider, model string) error {
	models, ok := r.catalog[provider]
	if !ok {
		return fmt.Errorf("provider %q not found", provider)
	}
	for _, m := range models {
		if m == model {
			r.defaultProvider = provider
			r.defaultModel = model
			return nil
		}
	}
	return fmt.Errorf("model %q not available in provider %q", model, provider)
}

func (r *mockSwitchableRegistry) ListAll() []llm.ModelInfo {
	names := make([]string, 0, len(r.catalog))
	for name := range r.catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []llm.ModelInfo
	for _, name := range names {
		for _, m := range r.catalog[name] {
			out = append(out, llm.ModelInfo{Name: m, Provider: name})
		}
	}
	return out
}

func (r *mockSwitchableRegistry) ListProviders() []llm.ProviderInfo {
	names := make([]string, 0, len(r.catalog))
	for name := range r.catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]llm.ProviderInfo, len(names))
	for i, n := range names {
		out[i] = llm.ProviderInfo{Name: n, Kind: "openai-compatible"}
	}
	return out
}

func (r *mockSwitchableRegistry) ListProviderModels(name string) ([]string, error) {
	models, ok := r.catalog[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	return models, nil
}

func newTestManagerWithSwitchableRegistry(t *testing.T, reg *mockSwitchableRegistry) (*SessionManager, string) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(st, reg, toolsReg, emergency, logger, cfg, permsEng, st)
	session, err := mgr.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return mgr, session.ID
}

func TestSwitchModel_BareNameInCurrentProviderUnchanged(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1", "a2"}, "beta": {"b1"}})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	if err := mgr.SwitchModel(context.Background(), sessionID, "a2"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if reg.defaultProvider != "alpha" || reg.defaultModel != "a2" {
		t.Errorf("provider/model = %s/%s, want alpha/a2", reg.defaultProvider, reg.defaultModel)
	}
}

func TestSwitchModel_ExplicitProviderSlashModelSwitchesBoth(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1"}, "beta": {"b1", "b2"}})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	if err := mgr.SwitchModel(context.Background(), sessionID, "beta/b2"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if reg.defaultProvider != "beta" || reg.defaultModel != "b2" {
		t.Errorf("provider/model = %s/%s, want beta/b2", reg.defaultProvider, reg.defaultModel)
	}
}

func TestSwitchModel_BareNameAutoDetectsOtherProvider(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1"}, "beta": {"b1", "only-on-beta"}})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	if err := mgr.SwitchModel(context.Background(), sessionID, "only-on-beta"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if reg.defaultProvider != "beta" || reg.defaultModel != "only-on-beta" {
		t.Errorf("provider/model = %s/%s, want beta/only-on-beta (auto-detected)", reg.defaultProvider, reg.defaultModel)
	}
}

func TestSwitchModel_BareNameAmbiguousAcrossProvidersErrors(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{
		"alpha": {"a1", "shared-name"},
		"beta":  {"b1", "shared-name"},
	})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	// "shared-name" IS in the current provider (alpha) too, so SetDefault
	// succeeds directly without ever reaching the ambiguity search — that's
	// correct: ambiguity only matters when the name isn't already
	// unambiguously resolved by "the current provider wins first".
	if err := mgr.SwitchModel(context.Background(), sessionID, "shared-name"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if reg.defaultProvider != "alpha" {
		t.Errorf("provider = %s, want alpha (current provider takes priority over search)", reg.defaultProvider)
	}
}

func TestSwitchModel_BareNameAmbiguousWhenNotInCurrentProvider(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{
		"alpha": {"a1"},
		"beta":  {"dup-name"},
		"gamma": {"dup-name"},
	})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	err := mgr.SwitchModel(context.Background(), sessionID, "dup-name")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "beta") || !strings.Contains(msg, "gamma") || !strings.Contains(msg, "multiple providers") {
		t.Errorf("error = %q, want it to name both beta and gamma and say \"multiple providers\"", msg)
	}
	// Nothing should have changed.
	if reg.defaultProvider != "alpha" || reg.defaultModel != "a1" {
		t.Errorf("provider/model = %s/%s after ambiguous switch, want unchanged alpha/a1", reg.defaultProvider, reg.defaultModel)
	}
}

func TestSwitchModel_BareNameNotFoundAnywhereReturnsOriginalError(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1"}, "beta": {"b1"}})
	mgr, sessionID := newTestManagerWithSwitchableRegistry(t, reg)

	if err := mgr.SwitchModel(context.Background(), sessionID, "totally-unknown"); err == nil {
		t.Fatal("expected an error for a model that exists nowhere")
	}
}

func TestSwitchModel_EmptySessionIDSkipsMetadataButStillSwitches(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1"}, "beta": {"b1"}})
	mgr, _ := newTestManagerWithSwitchableRegistry(t, reg)

	// No session at all (sessionless CLI switch — forge daemon set-provider).
	if err := mgr.SwitchModel(context.Background(), "", "beta/b1"); err != nil {
		t.Fatalf("SwitchModel with empty sessionID: %v", err)
	}
	if reg.defaultProvider != "beta" || reg.defaultModel != "b1" {
		t.Errorf("provider/model = %s/%s, want beta/b1", reg.defaultProvider, reg.defaultModel)
	}
}

func TestListProviders_Passthrough(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1"}, "beta": {"b1"}})
	mgr, _ := newTestManagerWithSwitchableRegistry(t, reg)

	got, err := mgr.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListProviders() len = %d, want 2", len(got))
	}
}

func TestListProviderModels_Passthrough(t *testing.T) {
	reg := newMockSwitchableRegistry("alpha", "a1", map[string][]string{"alpha": {"a1", "a2"}, "beta": {"b1"}})
	mgr, _ := newTestManagerWithSwitchableRegistry(t, reg)

	got, err := mgr.ListProviderModels("alpha")
	if err != nil {
		t.Fatalf("ListProviderModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListProviderModels(alpha) = %v, want 2 entries", got)
	}
}
