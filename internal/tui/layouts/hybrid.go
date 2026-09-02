package layouts

import (
	"strings"

	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// Hybrid renders minimal base + optional overlay sidebar.
// When showSidebar is true, the sidebar overlay is appended as a floating layer.
func Hybrid(transcript, input, footer string, sidebar components.SidebarModel, showSidebar bool) string {
	base := Minimal(transcript, input, footer)
	if !showSidebar {
		return base
	}
	overlay := sidebar.RenderOverlay()
	// For deterministic rendering (tests), place overlay below base with a separator.
	// Real TUI overlays would be positioned with lipgloss layers; for skeleton we
	// concatenate with a marker so golden snapshots are stable and TTY-free.
	return strings.Join([]string{base, "--- overlay ---", overlay}, "\n")
}
