package components

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	ansistrip "github.com/charmbracelet/x/ansi"
)

// TestRenderHighlightedCodeBlockPadsFullWidth is the regression lock for a
// real bug found trying this live in `forge tui`: each token's background
// only covers its own glyphs, so a code line shorter than the panel's width
// left the rest of the row uncolored — a "ragged" block instead of one
// solid rectangle. Every rendered row must reach exactly avail columns
// (gutter + content + background-only padding), never more, never less.
func TestRenderHighlightedCodeBlockPadsFullWidth(t *testing.T) {
	pal := testPalette()
	const avail = 60
	rows := renderHighlightedCodeBlock("python", "x = 1", avail, pal)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for a 1-line body, got %d: %q", len(rows), rows)
	}
	row := rows[0]

	if w := lipgloss.Width(row); w != avail {
		t.Errorf("row width = %d, want exactly %d (padded to fill the block)", w, avail)
	}
	// The padding itself must carry the elevated background, not just the
	// text — otherwise the fix would just extend transparent space.
	if !strings.Contains(row, "48;2;30;25;21") {
		t.Errorf("row missing elevated-bg SGR entirely, got %q", row)
	}
	stripped := ansistrip.Strip(row)
	if !strings.Contains(stripped, "x = 1") {
		t.Errorf("code content lost, stripped = %q", stripped)
	}
}
