package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TUIConfig holds the persisted TUI section inside .forge/config.json.
// It is stored alongside existing keys without breaking them.
type TUIConfig struct {
	Layout  string `json:"layout"`
	Palette string `json:"palette"`
	Sidebar bool   `json:"sidebar"`
}

// DefaultTUIConfig returns the default TUI configuration.
func DefaultTUIConfig() TUIConfig {
	return TUIConfig{
		Layout:  "hybrid",
		Palette: "ember",
		Sidebar: true,
	}
}

// IsValidLayout reports whether layout is one of the three known values.
func IsValidLayout(s string) bool {
	return s == "hybrid" || s == "session" || s == "minimal"
}

// IsValidPalette reports whether palette exists in the registry.
func IsValidPalette(s string) bool {
	_, ok := GetPalette(s)
	return ok
}

// LoadTUIConfig reads the tui section from path. If the file does not exist,
// is empty, or contains invalid JSON, defaults are returned without error.
// Missing tui section or missing fields are filled with defaults. Unknown
// sibling keys are ignored (preserved on save).
func LoadTUIConfig(path string) TUIConfig {
	cfg := DefaultTUIConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if len(data) == 0 {
		return cfg
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return cfg
	}
	raw, ok := doc["tui"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return cfg
	}
	var parsed TUIConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return cfg
	}
	// Fill defaults for empty fields.
	if parsed.Layout == "" {
		parsed.Layout = cfg.Layout
	}
	if parsed.Palette == "" {
		parsed.Palette = cfg.Palette
	}
	// Sidebar is a bool; we need to distinguish missing vs false. If the raw
	// does not contain "sidebar" key, keep default true. We check via map.
	var tmp map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tmp); err == nil {
		if _, has := tmp["sidebar"]; !has {
			parsed.Sidebar = cfg.Sidebar
		}
	}
	// Validate; invalid values fall back to defaults without error.
	if !IsValidLayout(parsed.Layout) {
		parsed.Layout = cfg.Layout
	}
	if !IsValidPalette(parsed.Palette) {
		parsed.Palette = cfg.Palette
	}
	return parsed
}

// SaveTUIConfig persists cfg's tui section into path atomically (write temp +
// rename), preserving all other keys in the document. If path does not exist,
// a new document is created. The file is written with 0o644 permissions.
func SaveTUIConfig(path string, cfg TUIConfig) error {
	// Validate before save; caller should have validated but we clamp anyway.
	if !IsValidLayout(cfg.Layout) {
		cfg.Layout = DefaultTUIConfig().Layout
	}
	if !IsValidPalette(cfg.Palette) {
		cfg.Palette = DefaultTUIConfig().Palette
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Read existing doc if any.
	var doc map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if jsonErr := json.Unmarshal(data, &doc); jsonErr != nil {
			// Invalid JSON — start fresh (defaults + tui section only)
			doc = make(map[string]json.RawMessage)
		}
	} else {
		doc = make(map[string]json.RawMessage)
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	doc["tui"] = json.RawMessage(raw)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// TUIConfigPath returns the project-scoped config path for TUI persistence.
// It mirrors internal/config.ProjectConfigPath but avoids importing that package
// to keep tui decoupled from core config validation. The path is ./.forge/config.json.
func TUIConfigPath() (string, error) {
	return filepath.Join(".forge", "config.json"), nil
}
