package tui

import "charm.land/bubbles/v2/key"

// KeyMap holds centralized key bindings for the TUI.
// All bindings are registered here with help strings so help views can be
// generated uniformly. Global bindings are handled before delegating to the
// textarea input area.
type KeyMap struct {
	ToggleSidebar key.Binding
	CycleLayout   key.Binding
	Quit          key.Binding
	Help          key.Binding
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
	}
}

// ShortHelp returns short help bindings.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ToggleSidebar, k.CycleLayout, k.Quit}
}

// FullHelp returns full help bindings grouped.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ToggleSidebar, k.CycleLayout, k.Quit, k.Help},
	}
}
