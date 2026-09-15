package components

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

// SidebarData is the single data model for the sidebar panel (TUI-6 redesign).
// It shows exactly three sections (owner-selected):
// (a) "Context & tokens": cumulative session tokens, turns in current context window.
// Documented limitation: window % and compaction status are not obtainable via existing RPC
// without daemon changes, so we show "turns in window: N" honestly and omit compaction.
// (b) "Plugins & skills": name + enabled/disabled per entry via plugin.list/skill.list.
// (c) "Turn stats": turn count this session, average latency (client-measured), last error truncated.
// Sessions list and model name were removed (redundant — both visible in footer).
type SidebarData struct {
	Palette       Palette
	TotalTokens   int
	TurnsInWindow int
	Plugins       []daemon.PluginInfoResult
	Skills        []daemon.SkillInfoResult
	TurnCount     int
	AvgLatency    string
	LastError     string
}

// SidebarModel renders the sidebar in two presentations: column vs overlay.
type SidebarModel struct {
	Data   SidebarData
	Width  int
	Height int
}

// NewSidebar creates a sidebar model.
func NewSidebar(pal Palette, w, h int) SidebarModel {
	return SidebarModel{Width: w, Height: h, Data: SidebarData{Palette: pal}}
}

// SetSize updates dimensions.
func (m *SidebarModel) SetSize(w, h int) {
	m.Width = w
	m.Height = h
}

// SetData updates sidebar data.
func (m *SidebarModel) SetData(d SidebarData) { m.Data = d }

// renderContent builds the inner content shared between both renderers — three sections.
func (m SidebarModel) renderContent() string {
	pal := m.Data.Palette
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text))
	faintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Faint))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Success))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Warning))

	// Section (a) Context & tokens
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Context & tokens"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("tokens: ") + textStyle.Render(formatTokensSidebar(m.Data.TotalTokens)))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("turns in window: ") + textStyle.Render(fmt.Sprintf("%d", m.Data.TurnsInWindow)))
	sb.WriteString("\n")
	// Documented: window % not obtainable without daemon changes — omitted honestly
	sb.WriteString(faintStyle.Render("(window % unavailable via RPC — shows local turns)"))
	sb.WriteString("\n\n")

	// Section (b) Plugins & skills
	sb.WriteString(titleStyle.Render("Plugins & skills"))
	sb.WriteString("\n")
	if len(m.Data.Plugins) == 0 && len(m.Data.Skills) == 0 {
		sb.WriteString(faintStyle.Render("(no plugins/skills)"))
		sb.WriteString("\n")
	} else {
		for _, p := range m.Data.Plugins {
			status := "disabled"
			style := dimStyle
			if p.Enabled {
				status = "enabled"
				style = successStyle
			}
			sb.WriteString(textStyle.Render(p.Name) + " " + style.Render("["+status+"]"))
			sb.WriteString("\n")
		}
		for _, s := range m.Data.Skills {
			status := "disabled"
			style := dimStyle
			if s.Enabled {
				status = "enabled"
				style = successStyle
			}
			line := s.Name + " " + style.Render("["+status+"]")
			if s.Category != "" {
				line += " " + dimStyle.Render("("+s.Category+")")
			}
			_ = warningStyle
			sb.WriteString(textStyle.Render(s.Name) + " " + style.Render("["+status+"]"))
			if s.Category != "" {
				// append category dim after
				sb.WriteString(" " + dimStyle.Render("("+s.Category+")"))
			}
			sb.WriteString("\n")
			_ = line
		}
	}
	sb.WriteString("\n")

	// Section (c) Turn stats
	sb.WriteString(titleStyle.Render("Turn stats"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("turns: ") + textStyle.Render(fmt.Sprintf("%d", m.Data.TurnCount)))
	sb.WriteString("\n")
	avg := m.Data.AvgLatency
	if avg == "" {
		avg = "—"
	}
	sb.WriteString(dimStyle.Render("avg latency: ") + textStyle.Render(avg))
	sb.WriteString("\n")
	lastErr := m.Data.LastError
	if lastErr == "" {
		lastErr = "—"
	} else if len(lastErr) > 60 {
		lastErr = lastErr[:57] + "..."
	}
	sb.WriteString(dimStyle.Render("last error: ") + faintStyle.Render(lastErr))
	sb.WriteString("\n")

	return sb.String()
}

func formatTokensSidebar(n int) string {
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

// RenderColumn renders as a permanent column (session layout).
func (m SidebarModel) RenderColumn() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.Data.Palette.Border)).
		Background(lipgloss.Color(m.Data.Palette.BGElevated)).
		Width(m.Width).
		Height(m.Height).
		Padding(1, 1)
	return style.Render(m.renderContent())
}

// RenderColumnCapped renders as a permanent column hard-capped to EXACTLY
// Height rows (2 border rows + content — no vertical padding, see below).
// lipgloss Height is only a minimum, so overlong card content would
// overflow the box and clip the footer — here overlong content is
// truncated and short content is padded, keeping the rail exactly
// railHeight rows as the TUI frame accounts.
// Capping by logical line COUNT alone isn't enough: a line wider than the
// available content width would still wrap to extra visual rows once
// Width-constrained by the style below, silently exceeding Height by
// however many lines wrapped (found via a card line like "(window %
// unavailable via RPC — shows local turns)", 50 chars against a ~28-char
// content width) — so every line is also truncated to that width first,
// guaranteeing one logical line is always exactly one rendered row.
// No vertical padding (only horizontal, for the text not to hug the
// border) deliberately: the rail's true minimum height must match the
// transcript viewport's own floor of 3 rows (SetSize) exactly, or a
// terminal short enough to hit that floor would size the rail taller than
// the transcript again — the exact frame-overflow bug this box style
// caused before (2 border + 2 padding rows meant this box could never
// render shorter than 5 rows, silently 2 more than the viewport's floor).
func (m SidebarModel) RenderColumnCapped() string {
	inner := m.Height - 2 // 2 border rows only
	if inner < 1 {
		inner = 1
	}
	innerWidth := m.Width - 4 // 2 border cols + 2 padding cols
	if innerWidth < 1 {
		innerWidth = 1
	}
	lines := strings.Split(m.renderContent(), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, innerWidth, "")
	}
	if len(lines) > inner {
		lines = lines[:inner]
	}
	for len(lines) < inner {
		lines = append(lines, "")
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.Data.Palette.Border)).
		Background(lipgloss.Color(m.Data.Palette.BGElevated)).
		Width(m.Width).
		Padding(0, 1)
	return style.Render(strings.Join(lines, "\n"))
}

// RenderOverlay renders as a floating overlay panel (hybrid/minimal layouts).
func (m SidebarModel) RenderOverlay() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.Data.Palette.Border)).
		Background(lipgloss.Color(m.Data.Palette.BGElevated)).
		Width(m.Width).
		Height(m.Height).
		Padding(1, 1)
	return style.Render(m.renderContent())
}
