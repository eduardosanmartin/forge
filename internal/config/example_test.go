package config

import (
	"path/filepath"
	"testing"
)

func TestExampleParsesAndMatchesCurrentSchema(t *testing.T) {
	examplePath := filepath.Join("..", "..", "configs", "forge.json.example")
	cfg, err := Load(examplePath)
	if err != nil {
		t.Fatalf("Load example %s: %v", examplePath, err)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("example schema_version = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example Validate: %v", err)
	}
	// Example must include the v4+ sections so drift is visible if removed.
	if cfg.TUI.Layout == "" || cfg.TUI.Palette == "" {
		t.Error("example tui section missing (layout/palette)")
	}
	if cfg.Limits.PluginWasmMaxBytes != DefaultPluginWasmMaxBytes {
		t.Errorf("example limits.plugin_wasm_max_bytes = %d, want %d", cfg.Limits.PluginWasmMaxBytes, DefaultPluginWasmMaxBytes)
	}
	if cfg.Agent.MaxIterations != DefaultAgentMaxIterations {
		t.Errorf("example agent.max_iterations = %d, want %d", cfg.Agent.MaxIterations, DefaultAgentMaxIterations)
	}
	if cfg.Agent.MaxTurnSeconds != DefaultAgentMaxTurnSeconds {
		t.Errorf("example agent.max_turn_seconds = %d, want %d", cfg.Agent.MaxTurnSeconds, DefaultAgentMaxTurnSeconds)
	}
	prov, ok := cfg.Providers["ollama"]
	if !ok {
		t.Fatal("example missing providers[ollama]")
	}
	if prov.ModelRoles["cheap"] == "" {
		t.Error("example providers[ollama].model_roles[cheap] missing (v4 field)")
	}
}
