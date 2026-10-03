// Code moved verbatim from model.go (M12 split) — same package, no behavior change.

package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func (m Model) cmdStatus() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.Status()
		return statusMsg{res: res, err: err}
	}
}

func (m Model) cmdExecuteTurn(msg string) tea.Cmd {
	if m.client == nil {
		return nil
	}
	sid := m.sessionID
	return func() tea.Msg {
		res, err := m.client.ExecuteTurn(sid, msg)
		return executeTurnMsg{res: res, err: err}
	}
}

func (m Model) cmdGetMessagesSince(since int) tea.Cmd {
	if m.client == nil {
		return nil
	}
	sid := m.sessionID
	return func() tea.Msg {
		res, err := m.client.GetMessagesSince(sid, since)
		return messagesSinceMsg{res: res, err: err}
	}
}

func (m Model) cmdHalt() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return haltResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.HaltSession(sid, "user halt via TUI")
		return haltResultMsg{err: err}
	}
}

// cmdHaltAll triggers the daemon's global emergency stop (RF-4.8,
// emergency.halt_all): every session's in-flight turn is cancelled, not
// just the current one. Wired to a double-Esc press — see the
// tea.KeyPressMsg handling and lastEscAt.
func (m Model) cmdHaltAll() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return haltAllResultMsg{err: fmt.Errorf("not connected")} }
	}
	return func() tea.Msg {
		err := m.client.HaltAll("emergency stop (double esc, TUI)")
		return haltAllResultMsg{err: err}
	}
}

func (m Model) cmdResume() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return resumeResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.ResumeSession(sid)
		return resumeResultMsg{err: err}
	}
}

func (m Model) cmdSwitchModel(name string) tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return switchModelResultMsg{model: name, err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.SwitchModel(sid, name)
		return switchModelResultMsg{model: name, err: err}
	}
}

func (m Model) cmdMarkSuccess() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return markSuccessResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.MarkSuccess(sid)
		return markSuccessResultMsg{err: err}
	}
}

func (m Model) cmdRefreshPlugins() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.PluginList()
		return pluginListMsg{res: res, err: err}
	}
}

func (m Model) cmdRefreshSkills() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.SkillList()
		return skillListMsg{res: res, err: err}
	}
}

func (m Model) cmdSubscribeEvents() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		ch, err := m.client.Events(context.Background())
		if err != nil {
			return eventErrorMsg{err: err}
		}
		// Return a sentinel that Update will use to store channel and start wait.
		// We use daemonEventMsg with empty notif as sentinel? Instead return a
		// custom message carrying the channel. Introduce eventsSubscribedMsg.
		return eventsSubscribedMsg{ch: ch}
	}
}

func (m Model) cmdWaitEvent(ch <-chan daemon.JSONRPCNotification) tea.Cmd {
	return func() tea.Msg {
		notif, ok := <-ch
		if !ok {
			return eventClosedMsg{}
		}
		return daemonEventMsg{notif: notif}
	}
}
