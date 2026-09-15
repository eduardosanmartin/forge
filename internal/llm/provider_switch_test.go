package llm

import (
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/logging"
)

// twoProviderRegistry builds a registry with two independent mock-backed
// providers ("alpha", "beta") for cross-provider switch/list tests. Callers
// must defer Close() on both returned mocks.
func twoProviderRegistry(t *testing.T, alphaModels, betaModels []string) (*Registry, *MockServer, *MockServer) {
	t.Helper()
	alpha := NewMockServer()
	alpha.SetDefaultResponse((&ModelsResponseBuilder{Models: alphaModels}).Build())
	beta := NewMockServer()
	beta.SetDefaultResponse((&ModelsResponseBuilder{Models: betaModels}).Build())

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "alpha",
		Providers: map[string]config.Provider{
			"alpha": {Kind: "openai-compatible", BaseURL: alpha.URL(), Models: alphaModels},
			"beta":  {Kind: "openai-compatible", BaseURL: beta.URL(), Models: betaModels},
		},
		Network: config.NetworkConfig{AllowedHosts: []string{hostFromURL(alpha.URL()), hostFromURL(beta.URL())}},
	}
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	reg, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg, alpha, beta
}

func TestRegistry_ListProviders(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1"})
	defer alpha.Close()
	defer beta.Close()

	got := reg.ListProviders()
	if len(got) != 2 {
		t.Fatalf("ListProviders() len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Errorf("ListProviders() = %+v, want sorted [alpha beta]", got)
	}
}

func TestRegistry_ListProviderModels_ForcesFreshFetch(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1"})
	defer alpha.Close()
	defer beta.Close()

	// The provider's catalog changes AFTER construction (e.g. the remote
	// service added a model since the daemon started) — an undeclared model
	// that ListProviderModels must still surface because it forces a fresh
	// fetch rather than serving the startup-time cache.
	alpha.SetDefaultResponse((&ModelsResponseBuilder{Models: []string{"a1", "a2-new"}}).Build())

	models, err := reg.ListProviderModels("alpha")
	if err != nil {
		t.Fatalf("ListProviderModels: %v", err)
	}
	found := false
	for _, m := range models {
		if m == "a2-new" {
			found = true
		}
	}
	if !found {
		t.Errorf("ListProviderModels(alpha) = %v, want it to include a2-new (proves a fresh fetch happened, not the startup cache)", models)
	}
}

func TestRegistry_ListProviderModels_UnknownProvider(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1"})
	defer alpha.Close()
	defer beta.Close()

	if _, err := reg.ListProviderModels("nope"); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestRegistry_SwitchProviderAndModel_Success(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1", "b2"})
	defer alpha.Close()
	defer beta.Close()

	if err := reg.SwitchProviderAndModel("beta", "b2"); err != nil {
		t.Fatalf("SwitchProviderAndModel: %v", err)
	}
	provider, model := reg.GetDefault()
	if model != "b2" {
		t.Errorf("GetDefault model = %q, want b2", model)
	}
	if provider == nil {
		t.Fatal("GetDefault provider is nil")
	}
}

func TestRegistry_SwitchProviderAndModel_ModelNotInTargetProvider(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1"})
	defer alpha.Close()
	defer beta.Close()

	if err := reg.SwitchProviderAndModel("beta", "a1"); err == nil {
		t.Fatal("expected an error switching to a model that belongs to a different provider")
	}
	// Defaults must be unchanged after a rejected switch.
	_, model := reg.GetDefault()
	if model != "a1" {
		t.Errorf("GetDefault model = %q after a rejected switch, want unchanged a1 (the constructor's own default)", model)
	}
}

func TestRegistry_SwitchProviderAndModel_UnknownProvider(t *testing.T) {
	reg, alpha, beta := twoProviderRegistry(t, []string{"a1"}, []string{"b1"})
	defer alpha.Close()
	defer beta.Close()

	if err := reg.SwitchProviderAndModel("nope", "a1"); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
