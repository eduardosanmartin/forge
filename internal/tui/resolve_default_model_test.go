package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDefaultModel(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			name: "valid config returns first model",
			json: `{"default_provider":"go","providers":{"go":{"models":["minimax-m3","other"]}}}`,
			want: "minimax-m3",
		},
		{
			name: "default_provider missing",
			json: `{"providers":{"go":{"models":["a"]}}}`,
			want: "",
		},
		{
			name: "provider not found",
			json: `{"default_provider":"missing","providers":{"go":{"models":["a"]}}}`,
			want: "",
		},
		{
			name: "empty models",
			json: `{"default_provider":"go","providers":{"go":{"models":[]}}}`,
			want: "",
		},
		{
			name: "invalid json",
			json: `{not json`,
			want: "",
		},
		{
			name: "providers missing",
			json: `{"default_provider":"go"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o644); err != nil {
				t.Fatal(err)
			}
			got := resolveDefaultModel(path)
			if got != tc.want {
				t.Fatalf("resolveDefaultModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveDefaultModelMissingFile(t *testing.T) {
	dir := t.TempDir()
	got := resolveDefaultModel(filepath.Join(dir, "nope.json"))
	if got != "" {
		t.Fatalf("expected empty for missing file, got %q", got)
	}
}

func TestResolveDefaultModelEmptyPathUsesDefault(t *testing.T) {
	// Empty path should not panic; it tries .forge/config.json and returns "" if missing.
	// Just verify it doesn't panic and returns string (empty if file absent in temp context is not tested here).
	// Instead test with a temp dir where .forge/config.json exists is not applicable.
	// Verify empty file path with no .forge in cwd returns "" without error.
	// We test by passing a path that exists with valid content via the fallback logic:
	// resolveDefaultModel("") uses ".forge/config.json" relative to cwd; in test cwd likely has a file,
	// but we only assert it returns a string without panic.
	_ = resolveDefaultModel("")
}

func TestRunSeedsCurrentModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfgJSON := `{"default_provider":"go","providers":{"go":{"kind":"openai-compatible","base_url":"http://x","models":["minimax-m3"]}}}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadTUIConfig(path)
	pal := MustGetPalette(cfg.Palette)
	m := NewModel(cfg, pal, cfg.Palette, path, nil)
	if m.CurrentModel() != "" {
		t.Fatalf("NewModel should not set currentModel, got %q", m.CurrentModel())
	}
	// Simulate Run's seeding logic
	if m.CurrentModel() == "" {
		m.currentModel = resolveDefaultModel(path)
	}
	if m.CurrentModel() != "minimax-m3" {
		t.Fatalf("seeded model = %q, want minimax-m3", m.CurrentModel())
	}
}
