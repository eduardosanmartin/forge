package config

import (
	"path/filepath"
	"testing"
)

func TestAgentMaxIterationsDefault(t *testing.T) {
	cfg := Defaults()
	if cfg.Agent.MaxIterations != DefaultAgentMaxIterations {
		t.Fatalf("default Agent.MaxIterations %d want %d", cfg.Agent.MaxIterations, DefaultAgentMaxIterations)
	}
	// Zero/negative normalized to default via Load
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfigFile(t, p, `{"agent":{"max_iterations":0}}`)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Agent.MaxIterations != DefaultAgentMaxIterations {
		t.Fatalf("zero should fallback to default, got %d", got.Agent.MaxIterations)
	}
	p2 := filepath.Join(dir, "config2.json")
	writeConfigFile(t, p2, `{"agent":{"max_iterations":-5}}`)
	got2, err := Load(p2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got2.Agent.MaxIterations != DefaultAgentMaxIterations {
		t.Fatalf("negative should fallback to default, got %d", got2.Agent.MaxIterations)
	}
}

func TestAgentMaxIterationsOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfigFile(t, p, `{"agent":{"max_iterations":3}}`)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Agent.MaxIterations != 3 {
		t.Fatalf("override want 3, got %d", got.Agent.MaxIterations)
	}
	// Save round-trip
	p3 := filepath.Join(dir, "save.json")
	cfg := Defaults()
	cfg.Agent.MaxIterations = 7
	if err := cfg.Save(p3); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(p3)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if loaded.Agent.MaxIterations != 7 {
		t.Fatalf("round-trip want 7, got %d", loaded.Agent.MaxIterations)
	}
}

func TestAgentMaxIterationsMissingPreservesDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfigFile(t, p, `{}`)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Agent.MaxIterations != DefaultAgentMaxIterations {
		t.Fatalf("missing should preserve default, got %d", got.Agent.MaxIterations)
	}
}
