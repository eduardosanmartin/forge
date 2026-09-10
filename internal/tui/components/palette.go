package components

import "charm.land/lipgloss/v2"

// Palette mirrors tui.Palette but lives in components to avoid import cycles.
// All hex values are centralized in tui/palette.go; this struct is just a
// data carrier for rendering.
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

func (p Palette) TextStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Text)) }
func (p Palette) DimStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Dim)) }
func (p Palette) FaintStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Faint)) }
func (p Palette) AccentStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)) }
func (p Palette) ErrorStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Error)) }
func (p Palette) SuccessStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(p.Success))
}
