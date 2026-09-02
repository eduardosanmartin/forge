package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	forgeconfig "github.com/eduardosanmartin/forge/internal/config"
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

// LoadTUIConfig reads the tui section from path via internal/config so parsing
// exists in exactly ONE place. This is the sole READ path for TUI settings.
//
//   - SaveTUIConfig is the sole WRITER of the tui section (atomic merge).
//   - LoadTUIConfig delegates to forgeconfig.Load which handles DisallowUnknownFields,
//     schema migration, and defaults. On any load error (missing file, empty,
//     invalid JSON) defaults are returned without error, matching legacy behavior.
//   - Invalid layout/palette values fall back to defaults after delegation.
//
// Documenting the split: read-via-config / write-via-tui keeps config authority
// centralized in internal/config while preserving TUI's atomic-merge save that
// must not clobber unknown sibling keys.
func LoadTUIConfig(path string) TUIConfig {
	def := DefaultTUIConfig()
	cfg, err := forgeconfig.Load(path)
	if err != nil {
		return def
	}
	// cfg is never nil when err == nil; but guard anyway.
	if cfg == nil {
		return def
	}
	tui := TUIConfig{
		Layout:  cfg.TUI.Layout,
		Palette: cfg.TUI.Palette,
		Sidebar: cfg.TUI.Sidebar,
	}
	// Fill defaults for empty fields (should not happen when loaded via
	// forgeconfig.Defaults, but keep for safety).
	if tui.Layout == "" {
		tui.Layout = def.Layout
	}
	if tui.Palette == "" {
		tui.Palette = def.Palette
	}
	// Sidebar is a bool where false is valid but also the zero value.
	// forgeconfig.Load via fileConfig clobbers the whole TUI struct, so a
	// document like {"tui":{"layout":"minimal"}} would yield Sidebar=false
	// even though the key was absent. Preserve default true when the key is
	// missing in the raw document.
	if data, readErr := os.ReadFile(path); readErr == nil && len(data) > 0 {
		var doc map[string]json.RawMessage
		if jsonErr := json.Unmarshal(data, &doc); jsonErr == nil {
			if raw, ok := doc["tui"]; ok && len(raw) > 0 && string(raw) != "null" {
				var tmp map[string]json.RawMessage
				if jsonErr2 := json.Unmarshal(raw, &tmp); jsonErr2 == nil {
					if _, has := tmp["sidebar"]; !has {
						tui.Sidebar = def.Sidebar
					}
				}
			} else if !ok {
				// No tui section at all — keep full defaults.
				tui = def
			}
		}
	}
	// Validate; invalid values fall back to defaults without error.
	if !IsValidLayout(tui.Layout) {
		tui.Layout = def.Layout
	}
	if !IsValidPalette(tui.Palette) {
		tui.Palette = def.Palette
	}
	return tui
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
// The path is ./.forge/config.json, resolved from the current working
// directory (consistent with how the daemon resolves its own config).
func TUIConfigPath() (string, error) {
	return filepath.Join(".forge", "config.json"), nil
}
