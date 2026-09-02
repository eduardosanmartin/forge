package layouts

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// Session renders the session layout: sidebar column + main area.
// Uses only palette tokens via styles (no hardcoded hex).
func Session(
	sidebar components.SidebarModel,
	transcript string,
	input string,
	footer string,
) string {
	// For TUI-1, simple vertical stacking with sidebar left.
	// We join sidebar and main content horizontally, then footer below.
	// transcript and input are already rendered strings.

	// Main column contains transcript + input
	mainContent := transcript + "\n" + input
	// Use lipgloss join.
	col := sidebar.RenderColumn()
	row := lipgloss.JoinHorizontal(lipgloss.Top, col, mainContent)
	return strings.Join([]string{row, footer}, "\n")
}
