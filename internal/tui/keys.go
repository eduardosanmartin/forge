package tui

import "charm.land/bubbles/v2/key"

// KeyMap holds centralized key bindings for the TUI.
// All bindings are registered here with help strings so help views can be
// generated uniformly. Global bindings are handled before delegating to the
// textarea input area.
//
// Reserved keys: ctrl+h (halt), ctrl+o (rail toggle — M2, ctrl+l is alias), ctrl+l (rail toggle alias),
// ctrl+c (quit), ctrl+p (suggestion navigation). New bindings must avoid them.
// shift+enter for newline depends on terminal enhanced-key reporting; ctrl+j is
// the portable alternative (emacs "accept-line").
// Mouse capture: shift+drag bypasses mouse reporting for text selection (Windows Terminal honors it).
// ctrl+m toggles mouse capture on/off at runtime with a footer toast.
// Rail panels: ctrl+1 Context & tokens, ctrl+2 Plugins & skills, ctrl+3 Turn stats, esc closes.
// Emergency stop: esc esc (two CONSECUTIVE presses within escDoubleTapWindow,
// no other key between them) halts EVERY session via emergency.halt_all —
// not a key.Binding since bubbles' key package has no double-press concept;
// handled directly in Update's tea.KeyPressMsg case (see lastEscAt).
// Error panel: an action/RPC error opens a floating panel with the full
// message instead of a footer toast (see showError); esc closes it, same
// priority tier as the rail/help/model panels.
type KeyMap struct {
	ToggleSidebar   key.Binding
	CycleLayout     key.Binding
	Quit            key.Binding
	Help            key.Binding
	Halt            key.Binding
	GrabSession     key.Binding
	ToggleMouse     key.Binding
	ShowContext     key.Binding
	ShowPlugins     key.Binding
	ShowTurnStats   key.Binding
}

// DefaultKeyMap returns the global key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		ToggleSidebar: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "toggle rail (M2)"),
		),
		CycleLayout: key.NewBinding(
			key.WithKeys("ctrl+l"),
			key.WithHelp("ctrl+l", "toggle rail (alias of ctrl+o)"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Halt: key.NewBinding(
			key.WithKeys("ctrl+h"),
			key.WithHelp("ctrl+h", "halt turn"),
		),
		GrabSession: key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "sessions (↑/↓, enter)"),
		),
		ToggleMouse: key.NewBinding(
			key.WithKeys("ctrl+m"),
			key.WithHelp("ctrl+m", "toggle mouse capture (text selection)"),
		),
		ShowContext: key.NewBinding(
			key.WithKeys("ctrl+1"),
			key.WithHelp("ctrl+1", "context panel"),
		),
		ShowPlugins: key.NewBinding(
			key.WithKeys("ctrl+2"),
			key.WithHelp("ctrl+2", "plugins panel"),
		),
		ShowTurnStats: key.NewBinding(
			key.WithKeys("ctrl+3"),
			key.WithHelp("ctrl+3", "turn stats panel"),
		),
	}
}

// ShortHelp returns short help bindings.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ToggleSidebar, k.CycleLayout, k.Quit, k.Halt, k.GrabSession, k.ToggleMouse}
}

// FullHelp returns full help bindings grouped.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleSidebar, k.CycleLayout, k.Quit, k.Help, k.Halt, k.GrabSession, k.ToggleMouse, k.ShowContext, k.ShowPlugins, k.ShowTurnStats},
	}
}
