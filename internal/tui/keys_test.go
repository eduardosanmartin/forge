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
	if len(sh) != 4 {
		t.Fatalf("ShortHelp len %d", len(sh))
	}
	fh := km.FullHelp()
	if len(fh) == 0 || len(fh[0]) != 5 {
		t.Fatalf("FullHelp shape %v", fh)
	}
}

func TestKeyMapDistinct(t *testing.T) {
	km := DefaultKeyMap()
	// Ensure no duplicate keys across bindings
	seen := map[string]string{}
	for _, b := range []key.Binding{km.ToggleSidebar, km.CycleLayout, km.Quit, km.Halt} {
		for _, k := range b.Keys() {
			if prev, ok := seen[k]; ok {
				t.Fatalf("duplicate key %q in %q and previous %q", k, b.Help().Desc, prev)
			}
			seen[k] = b.Help().Desc
		}
	}
}
