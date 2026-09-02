// Package tui implements the forge terminal TUI.
package tui

import (
	"sort"

	"charm.land/lipgloss/v2"
)

// Palette holds the centralized token set for the TUI.
// All layout and component code must consume colors via this palette;
// zero hardcoded hex values are allowed outside this file.
type Palette struct {
	BG         string
	BGElevated string
	Border     string
	Text       string
	Dim        string
	Faint      string
	Accent     string
	Success    string
	Warning    string
	Error      string
}

// Style helpers — each returns a lipgloss Style using the token color.

func (p Palette) BgStyle() lipgloss.Style         { return lipgloss.NewStyle().Background(lipgloss.Color(p.BG)) }
func (p Palette) BGElevatedStyle() lipgloss.Style { return lipgloss.NewStyle().Background(lipgloss.Color(p.BGElevated)) }
func (p Palette) BorderStyle() lipgloss.Style     { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Border)) }
func (p Palette) TextStyle() lipgloss.Style       { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text)) }
func (p Palette) DimStyle() lipgloss.Style        { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Dim)) }
func (p Palette) FaintStyle() lipgloss.Style      { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Faint)) }
func (p Palette) AccentStyle() lipgloss.Style     { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)) }
func (p Palette) SuccessStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success)) }
func (p Palette) WarningStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Warning)) }
func (p Palette) ErrorStyle() lipgloss.Style      { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)) }

// registry holds all known palettes. Adding a new palette is table-driven:
// insert one entry into this map. The ember palette is the default.
var registry = map[string]Palette{
	"ember": {
		BG:         "#161210",
		BGElevated: "#1e1915",
		Border:     "#332b24",
		Text:       "#e8e2da",
		Dim:        "#8a8178",
		Faint:      "#5c554e",
		Accent:     "#d97757",
		Success:    "#8aa672",
		Warning:    "#d9a257",
		Error:      "#c4544d",
	},
}

// GetPalette returns the palette for name and whether it was found.
func GetPalette(name string) (Palette, bool) {
	p, ok := registry[name]
	return p, ok
}

// MustGetPalette returns the palette for name or the default ember palette if
// name is unknown. This fallback is used for rendering when config contains an
// invalid palette name — it never crashes.
func MustGetPalette(name string) Palette {
	if p, ok := registry[name]; ok {
		return p
	}
	return registry["ember"]
}

// DefaultPalette returns the ember palette.
func DefaultPalette() Palette { return registry["ember"] }

// PaletteNames returns sorted palette names.
func PaletteNames() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
