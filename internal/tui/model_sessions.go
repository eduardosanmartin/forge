// Code moved verbatim from model.go (M12 split) — same package, no behavior change.

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

type listSessionsMsg struct {
	res *daemon.ListSessionsResult
	err error
}

type createSessionMsg struct {
	res *daemon.SessionResult
	err error
}

func (m Model) SessionID() string { return m.sessionID }

func (m Model) IsSessionFocus() bool { return m.sessionFocus || m.sessionsDropdownVisible }

func (m Model) SessionFocusIdx() int {
	if m.sessionsDropdownVisible {
		return m.sessionsDropdownIdx
	}
	return m.sessionFocusIdx
}

func (m Model) IsSessionsDropdownVisible() bool { return m.sessionsDropdownVisible }

func (m Model) SessionsDropdownIdx() int { return m.sessionsDropdownIdx }

func (m Model) cmdListSessions() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.ListSessions(10)
		return listSessionsMsg{res: res, err: err}
	}
}

func (m Model) cmdCreateSession() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.CreateSession()
		return createSessionMsg{res: res, err: err}
	}
}

func (m *Model) cycleSession() (bool, tea.Cmd) {
	if len(m.sessions) == 0 {
		m.toast = "no sessions"
		return false, nil
	}
	// Find current index
	idx := -1
	for i, s := range m.sessions {
		if s.ID == m.sessionID {
			idx = i
			break
		}
	}
	nextIdx := (idx + 1) % len(m.sessions)
	// If no current (idx -1) then nextIdx 0
	next := m.sessions[nextIdx]
	m.sessionID = next.ID
	// Reset lastSeq to 0 then apply via GetMessagesSince(0) — respect semantics: reset lastSeq to 0 then apply, local echo cleared.
	m.lastSeq = 0
	// For minimal viable: clear all entries and reload transcript via GetMessagesSince(0)
	// Spec says entries replaced, lastSeq reset, echo cleared — so clear entries wholesale.
	m.entries = nil
	m.pendingUserText = ""
	m.resetRunPanelForSessionSwitch()
	m.rebuildTranscriptForceBottom()
	m.toast = fmt.Sprintf("session → %s", next.ID[:8])
	// Also clear suggestions
	m.suggestionsVisible = false
	return true, m.cmdGetMessagesSince(0)
}

func (m *Model) switchToSession(idx int) (bool, tea.Cmd) {
	if len(m.sessions) == 0 || idx < 0 || idx >= len(m.sessions) {
		return false, nil
	}
	sel := m.sessions[idx]
	m.sessionID = sel.ID
	m.lastSeq = 0
	m.entries = nil
	m.pendingUserText = ""
	m.resetRunPanelForSessionSwitch()
	m.rebuildTranscriptForceBottom()
	m.toast = fmt.Sprintf("session → %s", sel.ID[:8])
	m.suggestionsVisible = false
	return true, m.cmdGetMessagesSince(0)
}

func (m Model) renderSessionsDropdown(maxRows int) string {
	var sb strings.Builder
	// Keep "Sessions ● focus" for backward compat with TUI-5 tests that assert this substring
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Sessions ● focus")
	// Window the list so the panel fits the transcript area: title + hint
	// take 2 rows, the caller box adds 2 border rows.
	budget := maxRows - 4
	if budget < 1 {
		budget = 1
	}
	n := len(m.sessions)
	start, end := windowRange(n, m.sessionsDropdownIdx, budget)
	if n > budget {
		title += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(
			fmt.Sprintf(" (%d/%d)", m.sessionsDropdownIdx+1, n))
	}
	sb.WriteString(title + "\n")
	if n == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no sessions)") + "\n")
	}
	for i := start; i < end; i++ {
		s := m.sessions[i]
		id := s.ID
		if len(id) > 12 {
			id = id[:12]
		}
		marker := "  "
		if s.ID == m.sessionID {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▶ ")
		}
		line := marker + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(id)
		if s.MessageCount > 0 {
			line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
		}
		isMarked := m.markedIDs != nil && m.markedIDs[s.ID]
		if !isMarked && s.Metadata != nil {
			if v, ok := s.Metadata["success"]; ok {
				if b, ok := v.(bool); ok && b {
					isMarked = true
				}
			}
		}
		if isMarked {
			line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Success)).Render(" ✓")
		}
		// Highlight selected index via background
		if i == m.sessionsDropdownIdx || (!m.sessionsDropdownVisible && i == m.sessionFocusIdx) {
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Foreground(lipgloss.Color(m.palette.Warning)).Render(marker+id) + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
			if isMarked {
				line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Success)).Render(" ✓")
			}
			// Wrap with background again for full line
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Render(line)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("↑/↓ navigate · enter select · esc close"))
	return sb.String()
}
