package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTUIConfigDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")
	cfg := LoadTUIConfig(path)
	if cfg.Layout != "hybrid" || cfg.Palette != "ember" || !cfg.Sidebar {
		t.Fatalf("defaults mismatch got %+v", cfg)
	}
}

func TestLoadTUIConfigDefaultsWhenEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTUIConfig(path)
	if cfg.Layout != "hybrid" {
		t.Fatalf("empty file should yield defaults, got %+v", cfg)
	}
}

func TestLoadTUIConfigDefaultsWhenPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.json")
	if err := os.WriteFile(path, []byte(`{"tui":{"layout":"minimal"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTUIConfig(path)
	if cfg.Layout != "minimal" {
		t.Fatalf("layout should be minimal got %q", cfg.Layout)
	}
	if cfg.Palette != "ember" {
		t.Fatalf("palette should default ember got %q", cfg.Palette)
	}
	if !cfg.Sidebar {
		t.Fatalf("sidebar should default true got %v", cfg.Sidebar)
	}
}

func TestLoadTUIConfigInvalidJSONReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTUIConfig(path)
	if cfg.Layout != "hybrid" {
		t.Fatalf("invalid json should yield defaults, got %+v", cfg)
	}
}

func TestSaveTUIConfigPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Initial file with providers + tui
	initial := map[string]any{
		"schema_version": 3,
		"providers": map[string]any{
			"ollama": map[string]any{"kind": "openai-compatible", "base_url": "http://127.0.0.1:11434/v1", "models": []string{"m1"}},
		},
		"tui": map[string]any{"layout": "hybrid", "palette": "ember", "sidebar": true},
	}
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Save new tui section
	newCfg := TUIConfig{Layout: "session", Palette: "ember", Sidebar: false}
	if err := SaveTUIConfig(path, newCfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Reload raw doc
	raw, _ := os.ReadFile(path)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["providers"]; !ok {
		t.Fatal("providers key dropped")
	}
	if _, ok := doc["schema_version"]; !ok {
		t.Fatal("schema_version dropped")
	}
	var tuiSec TUIConfig
	if err := json.Unmarshal(doc["tui"], &tuiSec); err != nil {
		t.Fatal(err)
	}
	if tuiSec.Layout != "session" || tuiSec.Sidebar != false {
		t.Fatalf("tui section not updated %+v", tuiSec)
	}
}

func TestSaveTUIConfigAtomicNoTempResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := SaveTUIConfig(path, DefaultTUIConfig()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp*"))
	if len(matches) != 0 {
		t.Fatalf("temp residue %v", matches)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("final file missing: %v", err)
	}
}

func TestSaveTUIConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "round.json")
	cfg := TUIConfig{Layout: "minimal", Palette: "ember", Sidebar: true}
	if err := SaveTUIConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	got := LoadTUIConfig(path)
	if got != cfg {
		t.Fatalf("roundtrip mismatch got %+v want %+v", got, cfg)
	}
}

func TestIsValidLayoutTable(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hybrid", true}, {"session", true}, {"minimal", true}, {"bad", false}, {"", false},
	}
	for _, tc := range cases {
		if got := IsValidLayout(tc.in); got != tc.want {
			t.Errorf("IsValidLayout(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestTUIConfigPathNotEmpty(t *testing.T) {
	p, err := TUIConfigPath()
	if err != nil || p == "" {
		t.Fatalf("path empty err %v", err)
	}
}
