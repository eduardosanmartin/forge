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
	Tokens      int // cumulative session tokens; 0 hides the field
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
	// Right: session short + daemon addr + tokens
	rightParts := []string{}
	if m.SessionID != "" {
		short := m.SessionID
		if len(short) > 8 {
			short = short[:8]
		}
		rightParts = append(rightParts, styleAccent.Render(short))
	}
	if m.Tokens > 0 {
		rightParts = append(rightParts, styleFaint.Render(formatTokens(m.Tokens)+" tokens"))
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

// formatTokens formats n with thousands separators, e.g. 12431 -> "12,431".
func formatTokens(n int) string {
	if n < 0 {
		n = 0
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
		if len(s) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(s); i += 3 {
		out = append(out, s[i:i+3]...)
		if i+3 < len(s) {
			out = append(out, ',')
		}
	}
	return string(out)
}
