package tui

import "charm.land/bubbles/v2/key"

// KeyMap holds centralized key bindings for the TUI.
// All bindings are registered here with help strings so help views can be
// generated uniformly. Global bindings are handled before delegating to the
// textarea input area.
//
// Reserved keys: ctrl+h (halt), ctrl+o (sidebar), ctrl+l (layout),
// ctrl+c (quit), ctrl+p (suggestion navigation). New bindings must avoid them.
// shift+enter for newline depends on terminal enhanced-key reporting; ctrl+j is
// the portable alternative (emacs "accept-line").
type KeyMap struct {
	ToggleSidebar key.Binding
	CycleLayout   key.Binding
	Quit          key.Binding
	Help          key.Binding
	Halt          key.Binding
	GrabSession   key.Binding
}

// DefaultKeyMap returns the global key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		ToggleSidebar: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("ctrl+o", "toggle sidebar"),
		),
		CycleLayout: key.NewBinding(
			key.WithKeys("ctrl+l"),
			key.WithHelp("ctrl+l", "cycle layout"),
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
			key.WithHelp("ctrl+g", "next session"),
		),
	}
}

// ShortHelp returns short help bindings.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ToggleSidebar, k.CycleLayout, k.Quit, k.Halt, k.GrabSession}
}

// FullHelp returns full help bindings grouped.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleSidebar, k.CycleLayout, k.Quit, k.Help, k.Halt, k.GrabSession},
	}
}
