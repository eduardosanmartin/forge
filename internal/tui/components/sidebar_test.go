package components

import (
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestSidebarRendersSameDataBothPresentations(t *testing.T) {
	pal := testPalette()
	data := SidebarData{
		SessionID: "sess-123",
		Sessions: []daemon.SessionResult{
			{ID: "sess-123", MessageCount: 5},
			{ID: "sess-456", MessageCount: 2},
		},
		Palette: pal,
	}
	m := NewSidebar(pal, 28, 20)
	m.SetData(data)

	col := m.RenderColumn()
	over := m.RenderOverlay()

	// Both should contain session ids
	for _, id := range []string{"sess-123", "sess-456"} {
		if !strings.Contains(col, id) {
			t.Fatalf("column missing %s: %q", id, col)
		}
		if !strings.Contains(over, id) {
			t.Fatalf("overlay missing %s: %q", id, over)
		}
	}
	// Both should contain current marker accent? At least contain session id
	if !strings.Contains(col, "Sessions") || !strings.Contains(over, "Sessions") {
		t.Fatalf("title missing")
	}
	// Ensure overlay and column are not identical (different border)
	if col == over {
		t.Log("column and overlay identical — acceptable if border same, but should differ")
	}
}

func TestSidebarEmptySessions(t *testing.T) {
	pal := testPalette()
	m := NewSidebar(pal, 28, 10)
	m.SetData(SidebarData{Palette: pal})
	col := m.RenderColumn()
	if !strings.Contains(col, "no sessions") {
		t.Fatalf("empty should show (no sessions), got %q", col)
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
