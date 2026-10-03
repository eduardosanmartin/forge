// Code moved verbatim from model.go (M12 split) — same package, no behavior change.

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// permissionResponder is implemented by clients that can answer
// permission requests (the real ClientAdapter); test doubles without it
// simply can't answer, and the daemon denies on timeout.
type permissionResponder interface {
	PermissionRespond(requestID, decision string) error
}

type permRespondMsg struct{ err error }

func (m Model) cmdPermissionRespond(requestID, decision string) tea.Cmd {
	pr, ok := m.client.(permissionResponder)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return permRespondMsg{err: pr.PermissionRespond(requestID, decision)}
	}
}

func (m *Model) dropPermission(requestID string) {
	for i, p := range m.permQueue {
		if p.RequestID == requestID {
			m.permQueue = append(m.permQueue[:i], m.permQueue[i+1:]...)
			return
		}
	}
}

// renderPermissionPrompt draws the modal for the oldest pending request.
func (m Model) renderPermissionPrompt() string {
	req := m.permQueue[0]
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Warning)).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim))
	var sb strings.Builder
	sb.WriteString(warn.Render("⚠ permission requested") + "\n\n")
	sb.WriteString(req.Summary + "\n")
	sb.WriteString(dim.Render(req.Rule))
	if n := len(m.permQueue); n > 1 {
		sb.WriteString(dim.Render(fmt.Sprintf("  ·  %d pending", n)))
	}
	sb.WriteString("\n\n[y] allow once   [s] allow for this session   [n/esc] deny")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Warning)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
	return box.Render(sb.String())
}
