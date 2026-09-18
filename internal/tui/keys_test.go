package tui

import (
	"testing"

	"charm.land/bubbles/v2/key"
)

func TestDefaultKeyMapBindings(t *testing.T) {
	km := DefaultKeyMap()
	if !km.ToggleSidebar.Enabled() || !km.CycleLayout.Enabled() || !km.Quit.Enabled() || !km.Halt.Enabled() {
		t.Fatal("default key bindings should be enabled")
	}
	if len(km.ToggleSidebar.Keys()) == 0 || km.ToggleSidebar.Keys()[0] != "ctrl+o" {
		t.Fatalf("ToggleSidebar keys %v", km.ToggleSidebar.Keys())
	}
	if km.CycleLayout.Keys()[0] != "ctrl+l" {
		t.Fatalf("CycleLayout keys %v", km.CycleLayout.Keys())
	}
	if km.Quit.Keys()[0] != "ctrl+c" {
		t.Fatalf("Quit keys %v", km.Quit.Keys())
	}
	if km.Halt.Keys()[0] != "ctrl+h" {
		t.Fatalf("Halt keys %v", km.Halt.Keys())
	}
	help := km.ToggleSidebar.Help()
	if help.Desc == "" || help.Key == "" {
		t.Fatal("help strings missing")
	}
}

func TestKeyMapShortHelp(t *testing.T) {
	km := DefaultKeyMap()
	sh := km.ShortHelp()
	if len(sh) != 6 {
		t.Fatalf("ShortHelp len %d", len(sh))
	}
	fh := km.FullHelp()
	if len(fh) == 0 || len(fh[0]) != 11 {
		t.Fatalf("FullHelp shape %v", fh)
	}
}

func TestKeyMapDistinct(t *testing.T) {
	km := DefaultKeyMap()
	// Ensure no duplicate keys across bindings (including ctrl+g); must avoid reserved set ctrl+h, ctrl+o, ctrl+l, ctrl+c, ctrl+p
	seen := map[string]string{}
	for _, b := range []key.Binding{km.ToggleSidebar, km.CycleLayout, km.Quit, km.Halt, km.GrabSession, km.ToggleMouse, km.ShowContext, km.ShowPlugins, km.ShowTurnStats, km.ShowRunPanel} {
		for _, k := range b.Keys() {
			if prev, ok := seen[k]; ok {
				t.Fatalf("duplicate key %q in %q and previous %q", k, b.Help().Desc, prev)
			}
			seen[k] = b.Help().Desc
		}
	}
	// Verify ctrl+p not used (reserved for suggestion nav)
	for _, b := range []key.Binding{km.ToggleSidebar, km.CycleLayout, km.Quit, km.Halt, km.GrabSession, km.ToggleMouse} {
		for _, k := range b.Keys() {
			if k == "ctrl+p" {
				t.Fatal("ctrl+p is reserved and should not be used")
			}
		}
	}
}
