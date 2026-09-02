package components

import (
	"fmt"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"charm.land/lipgloss/v2"
)

// SidebarData is the single data model for the sidebar panel.
// ModelName is the current model (from ExecuteTurnResult.Model or fallback).
// Documented source: last successful ExecuteTurnResult.Model when present; otherwise empty (no config model schema in TUI-4).
// Focused indicates session focus mode (arrow navigation); FocusIdx is selected index.
type SidebarData struct {
	SessionID string
	Sessions  []daemon.SessionResult
	Palette   Palette
	MarkedIDs map[string]bool // sessions flagged as success via /mark
	ModelName string
	Focused   bool
	FocusIdx  int
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

// renderContent builds the inner content shared between both renderers.
func (m SidebarModel) renderContent() string {
	pal := m.Data.Palette
	titleText := "Sessions"
	if m.Data.Focused {
		titleText = "Sessions ● focus"
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Bold(true).Render(titleText)
	var body string
	if len(m.Data.Sessions) == 0 {
		body = lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Faint)).Render("(no sessions)")
	} else {
		for i, s := range m.Data.Sessions {
			id := s.ID
			if len(id) > 12 {
				id = id[:12]
			}
			marker := "  "
			if s.ID == m.Data.SessionID {
				marker = lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Render("▶ ")
			}
			// In focus mode, highlight selected index
			if m.Data.Focused && i == m.Data.FocusIdx {
				marker = lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Warning)).Render("▸ ")
			}
			line := marker + lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text)).Render(id)
			if m.Data.Focused && i == m.Data.FocusIdx {
				line = lipgloss.NewStyle().Background(lipgloss.Color(pal.BGElevated)).Foreground(lipgloss.Color(pal.Warning)).Render(marker + id)
				// Use focus highlight; keep marked etc after
				if s.MessageCount > 0 {
					line += lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
				}
				isMarked := m.Data.MarkedIDs != nil && m.Data.MarkedIDs[s.ID]
				if !isMarked && s.Metadata != nil {
					if v, ok := s.Metadata["success"]; ok {
						if b, ok := v.(bool); ok && b {
							isMarked = true
						}
					}
				}
				if isMarked {
					line += lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Success)).Render(" ✓")
				}
				line = lipgloss.NewStyle().Background(lipgloss.Color(pal.BGElevated)).Render(line)
			} else {
				if s.MessageCount > 0 {
					line += lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
				}
				// Success flag: metadata success or explicit MarkedIDs.
				isMarked := m.Data.MarkedIDs != nil && m.Data.MarkedIDs[s.ID]
				if !isMarked && s.Metadata != nil {
					if v, ok := s.Metadata["success"]; ok {
						if b, ok := v.(bool); ok && b {
							isMarked = true
						}
					}
				}
				if isMarked {
					line += lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Success)).Render(" ✓")
				}
			}
			body += line + "\n"
		}
	}
	current := ""
	if m.Data.SessionID != "" {
		short := m.Data.SessionID
		if len(short) > 12 {
			short = short[:12]
		}
		current = lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim)).Render("current: ") + lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text)).Render(short)
	}
	modelLine := ""
	if m.Data.ModelName != "" {
		modelLine = lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim)).Render("model: ") + lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text)).Render(m.Data.ModelName)
	}
	content := title + "\n" + body
	if current != "" {
		content += "\n" + current
	}
	if modelLine != "" {
		content += "\n" + modelLine
	}
	return content
}

// RenderColumn renders as a permanent column (session layout).
func (m SidebarModel) RenderColumn() string {
	borderColor := m.Data.Palette.Border
	if m.Data.Focused {
		borderColor = m.Data.Palette.Accent
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Background(lipgloss.Color(m.Data.Palette.BGElevated)).
		Width(m.Width).
		Height(m.Height).
		Padding(1, 1)
	return style.Render(m.renderContent())
}

// RenderOverlay renders as a floating overlay panel (hybrid/minimal layouts).
func (m SidebarModel) RenderOverlay() string {
	borderColor := m.Data.Palette.Border
	if m.Data.Focused {
		borderColor = m.Data.Palette.Accent
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Background(lipgloss.Color(m.Data.Palette.BGElevated)).
		Width(m.Width).
		Height(m.Height).
		Padding(1, 1)
	return style.Render(m.renderContent())
}
