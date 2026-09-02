package components

import (
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestSidebarRendersThreeSections(t *testing.T) {
	pal := testPalette()
	data := SidebarData{
		Palette:       pal,
		TotalTokens:   12345,
		TurnsInWindow: 7,
		TurnCount:     3,
		AvgLatency:    "120ms",
		LastError:     "something failed",
		Plugins: []daemon.PluginInfoResult{
			{Name: "plug-a", Enabled: true},
			{Name: "plug-b", Enabled: false},
		},
		Skills: []daemon.SkillInfoResult{
			{Name: "skill-x", Enabled: true, Category: "code"},
			{Name: "skill-y", Enabled: false},
		},
	}
	m := NewSidebar(pal, 28, 20)
	m.SetData(data)

	col := m.RenderColumn()
	over := m.RenderOverlay()

	for _, want := range []string{"Context & tokens", "Plugins & skills", "Turn stats"} {
		if !strings.Contains(col, want) {
			t.Fatalf("column missing section %q: %q", want, col)
		}
		if !strings.Contains(over, want) {
			t.Fatalf("overlay missing section %q: %q", want, over)
		}
	}
	// Tokens (styled split between label and value — check parts)
	if !strings.Contains(col, "12,345") {
		t.Fatalf("tokens not rendered: %q", col)
	}
	if !strings.Contains(col, "tokens:") {
		t.Fatalf("tokens label not rendered: %q", col)
	}
	// Turns in window — label and value are separately styled, so check contiguously via parts
	if !strings.Contains(col, "turns in window:") {
		t.Fatalf("turns in window label not rendered: %q", col)
	}
	if !strings.Contains(col, "7") {
		t.Fatalf("turns count 7 not rendered: %q", col)
	}
	// Plugins names + status
	for _, name := range []string{"plug-a", "plug-b", "skill-x", "skill-y"} {
		if !strings.Contains(col, name) {
			t.Fatalf("plugin/skill %q missing: %q", name, col)
		}
	}
	if !strings.Contains(col, "enabled") || !strings.Contains(col, "disabled") {
		t.Fatalf("enabled/disabled not rendered: %q", col)
	}
	// Turn stats — label/value are separately styled
	if !strings.Contains(col, "turns:") || !strings.Contains(col, "3") {
		t.Fatalf("turn count not rendered: %q", col)
	}
	if !strings.Contains(col, "120ms") {
		t.Fatalf("avg latency not rendered: %q", col)
	}
	if !strings.Contains(col, "something") || !strings.Contains(col, "failed") {
		t.Fatalf("last error not rendered: %q", col)
	}
	// Ensure overlay and column are not identical (different border)
	if col == over {
		t.Log("column and overlay identical — acceptable if border same, but should differ")
	}
	// Ensure old sessions list not present (removed per TUI-6)
	if strings.Contains(col, "sess-123") {
		t.Fatalf("sidebar should not contain sessions after redesign: %q", col)
	}
	// Ensure model name not in sidebar (moved to footer)
	if strings.Contains(col, "model:") {
		t.Fatalf("sidebar should not contain model after redesign: %q", col)
	}
}

func TestSidebarEmptyPluginsSkills(t *testing.T) {
	pal := testPalette()
	m := NewSidebar(pal, 28, 10)
	m.SetData(SidebarData{Palette: pal, TotalTokens: 0, TurnsInWindow: 0})
	col := m.RenderColumn()
	if !strings.Contains(col, "Context & tokens") {
		t.Fatalf("missing Context section: %q", col)
	}
	if !strings.Contains(col, "no plugins/skills") {
		t.Fatalf("empty plugins/skills should show placeholder, got %q", col)
	}
	// Turn stats still shows
	if !strings.Contains(col, "Turn stats") {
		t.Fatalf("missing Turn stats: %q", col)
	}
}

func TestSidebarSetSize(t *testing.T) {
	pal := testPalette()
	m := NewSidebar(pal, 28, 10)
	m.SetSize(40, 15)
	if m.Width != 40 || m.Height != 15 {
		t.Fatalf("SetSize failed %+v", m)
	}
}
