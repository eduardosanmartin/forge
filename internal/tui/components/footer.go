package components

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"
)

// FooterModel renders the footer bar.
type FooterModel struct {
	Palette     Palette
	Width       int
	Cwd         string
	SessionID   string
	DaemonAddr  string
	Version     string
	Toast       string
	DaemonErr   string
	ShowSpinner bool
}

// NewFooter creates a footer model.
func NewFooter(pal Palette, w int) FooterModel {
	cwd, _ := os.Getwd()
	return FooterModel{Palette: pal, Width: w, Cwd: cwd}
}

// SetSize updates width.
func (m *FooterModel) SetSize(w int) { m.Width = w }

// Render builds the footer string using palette tokens.
func (m FooterModel) Render() string {
	styleBorder := lipgloss.NewStyle().Foreground(lipgloss.Color(m.Palette.Border))
	styleDim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.Palette.Dim))
	styleFaint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.Palette.Faint))
	styleAccent := lipgloss.NewStyle().Foreground(lipgloss.Color(m.Palette.Accent))
	styleError := lipgloss.NewStyle().Foreground(lipgloss.Color(m.Palette.Error))

	// Left: cwd
	left := styleDim.Render(m.Cwd)
	// Right: session short + daemon addr
	rightParts := []string{}
	if m.SessionID != "" {
		short := m.SessionID
		if len(short) > 8 {
			short = short[:8]
		}
		rightParts = append(rightParts, styleAccent.Render(short))
	}
	if m.DaemonAddr != "" {
		rightParts = append(rightParts, styleFaint.Render(m.DaemonAddr))
	}
	if m.Version != "" {
		rightParts = append(rightParts, styleFaint.Render(m.Version))
	}
	if m.DaemonErr != "" {
		rightParts = append(rightParts, styleError.Render(m.DaemonErr))
	}
	right := ""
	for i, p := range rightParts {
		if i > 0 {
			right += "  "
		}
		right += p
	}
	// Toast line above footer if present
	toastLine := ""
	if m.Toast != "" {
		toastLine = styleError.Render(m.Toast) + "\n"
	}
	spinner := ""
	if m.ShowSpinner {
		spinner = styleAccent.Render(" ⠋ working…") + " "
	}

	// Simple two-column layout within width
	// Use lipgloss to join.
	sep := " │ "
	content := left
	if right != "" {
		// Pad between left and right
		content = left + spinner + styleBorder.Render(sep) + right
	} else if spinner != "" {
		content += spinner
	}
	// Ensure footer fits width; truncate left if needed.
	_ = fmt.Sprintf // avoid unused

	bar := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.Palette.Border)).
		Background(lipgloss.Color(m.Palette.BGElevated)).
		Width(m.Width).
		Render(content)

	if toastLine != "" {
		return toastLine + bar
	}
	return bar
}
