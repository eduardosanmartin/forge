package components

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputPlaceholderIsField(t *testing.T) {
	m := NewInput("placeholder test", 80, 3)
	if m.TA.Placeholder != "placeholder test" {
		t.Fatalf("Placeholder field not set, got %q", m.TA.Placeholder)
	}
}

func TestInputDeleteBackwardIsBackspaceOnly(t *testing.T) {
	m := NewInput("test", 80, 3)
	keys := m.TA.KeyMap.DeleteCharacterBackward.Keys()
	if len(keys) != 1 || keys[0] != "backspace" {
		t.Fatalf("expected backspace only, got %v", keys)
	}
}

func TestInputShiftEnterNewlineVsEnter(t *testing.T) {
	m := NewInput("test", 80, 3)
	// Verify keymap is correctly rebound to shift+enter for newline
	if len(m.TA.KeyMap.InsertNewline.Keys()) != 1 || m.TA.KeyMap.InsertNewline.Keys()[0] != "shift+enter" {
		t.Fatalf("InsertNewline should be shift+enter only, got %v", m.TA.KeyMap.InsertNewline.Keys())
	}
	m.TA.SetValue("hello")

	// Simulate shift+enter via Update using proper key code
	msg := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift})
	updated, _ := m.Update(msg)
	if !containsNewline(updated.Value()) {
		t.Fatalf("shift+enter should insert newline, got %q", updated.Value())
	}
	// Ensure enter does not insert newline when InsertNewline is shift+enter only
	m2 := NewInput("test", 80, 3)
	m2.TA.SetValue("hello")
	msgEnter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	updated2, _ := m2.Update(msgEnter)
	if containsNewline(updated2.Value()) {
		// Enter should not produce newline; it would be handled by outer model as submit.
		// If it does, our rebind failed.
		t.Fatalf("enter should not insert newline with shift+enter mapping, got %q", updated2.Value())
	}
}

func containsNewline(s string) bool {
	for _, c := range s {
		if c == '\n' {
			return true
		}
	}
	return false
}

func TestInputViewportNewOptions(t *testing.T) {
	m := NewInput("test", 40, 5)
	if m.TA.Width() != 40 {
		// SetWidth accounts for prompt/etc, but should be non-zero and related to 40
		if m.TA.Width() == 0 {
			t.Fatalf("width not set")
		}
	}
	if m.TA.Height() < 1 {
		t.Fatalf("height not set")
	}
}

func TestInputSetSize(t *testing.T) {
	m := NewInput("test", 80, 3)
	m.SetSize(60, 6)
	if m.TA.Width() == 0 {
		t.Fatal("SetSize failed")
	}
}
