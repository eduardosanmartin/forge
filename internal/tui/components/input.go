package components

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
)

// InputModel wraps a textarea for the prompt input area.
// Uses textarea (not textinput) so shift+enter inserts newlines.
type InputModel struct {
	TA textarea.Model
}

// NewInput creates an input model with placeholder and keymap adjustments.
// Rebind DeleteCharacterBackward to backspace-only so ctrl+h remains free.
func NewInput(placeholder string, w, h int) InputModel {
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.SetWidth(w)
	ta.SetHeight(h)
	ta.Focus()

	// Rebind DeleteCharacterBackward to backspace only (free ctrl+h) and
	// InsertNewline to shift+enter + ctrl+j (enter is handled globally as send).
	// ctrl+j is the standard emacs "accept-line" alternative for terminals
	// that do not distinguish shift+enter via enhanced-key reporting.
	km := ta.KeyMap
	km.DeleteCharacterBackward = key.NewBinding(key.WithKeys("backspace"), key.WithHelp("backspace", "delete character backward"))
	km.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp("shift+enter, ctrl+j", "newline (shift+enter requires enhanced keys)"))
	ta.KeyMap = km

	return InputModel{TA: ta}
}

// SetSize updates dimensions.
func (m *InputModel) SetSize(w, h int) {
	m.TA.SetWidth(w)
	m.TA.SetHeight(h)
}

// Value returns current text.
func (m InputModel) Value() string { return m.TA.Value() }

// Reset clears the input.
func (m *InputModel) Reset() { m.TA.Reset() }

// SetValue replaces the input text.
func (m *InputModel) SetValue(s string) { m.TA.SetValue(s) }

// KeyMap exposes the underlying textarea keymap for inspection and adjustment.
func (m InputModel) KeyMap() textarea.KeyMap { return m.TA.KeyMap }

// Focus focuses the textarea.
func (m *InputModel) Focus() tea.Cmd { return m.TA.Focus() }

// Blur blurs the textarea.
func (m *InputModel) Blur() { m.TA.Blur() }

// Update delegates to the textarea. Enter is intentionally NOT handled here:
// the top-level model intercepts enter as "send" before delegating, while the
// textarea's InsertNewline is rebound to shift+enter for multiline input.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.TA, cmd = m.TA.Update(msg)
	return m, cmd
}

// View renders the input area.
func (m InputModel) View() string { return m.TA.View() }
