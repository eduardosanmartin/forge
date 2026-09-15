package components

import (
	"strings"
	"testing"
)

func TestFooterRender(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.SessionID = "sess-abcdef1234567890"
	m.DaemonAddr = "127.0.0.1:7777"
	content := m.Render()
	if !strings.Contains(content, "sess-abc") { // short id 8 chars
		t.Fatalf("footer missing short session id, got %q", content)
	}
	if !strings.Contains(content, "127.0.0.1:7777") {
		t.Fatalf("footer missing daemon addr")
	}
}

func TestFooterToastAndSpinner(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.Toast = "something failed"
	m.ShowSpinner = true
	content := m.Render()
	if !strings.Contains(content, "something failed") {
		t.Fatalf("toast missing")
	}
	if !strings.Contains(content, "working") {
		t.Fatalf("spinner missing")
	}
}

// TestFooterWorkingStats confirms the live elapsed/token estimate (item
// 21) renders next to the spinner — visible even when the transcript is
// scrolled away from the pending message's own Working marker.
func TestFooterWorkingStats(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.Cwd = "~/proj" // short cwd: leave enough width for the stats, not truncated
	m.ShowSpinner = true
	m.WorkingStats = "8,5s · 612 tokens"
	content := m.Render()
	if !strings.Contains(content, "working… (8,5s · 612 tokens)") {
		t.Fatalf("footer should show the live stats next to the spinner, got %q", content)
	}
}

// TestFooterNoWorkingStatsBeforeFirstTick confirms an empty WorkingStats
// (no tick has landed yet) renders the plain "working…" label without a
// dangling empty "()".
func TestFooterNoWorkingStatsBeforeFirstTick(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.ShowSpinner = true
	content := m.Render()
	if strings.Contains(content, "()") {
		t.Fatalf("footer should not show an empty parenthetical before the first tick, got %q", content)
	}
	if !strings.Contains(content, "working…") {
		t.Fatalf("footer should still show the plain working label, got %q", content)
	}
}

func TestFooterDaemonErr(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.DaemonErr = "daemon unreachable"
	content := m.Render()
	if !strings.Contains(content, "daemon unreachable") {
		t.Fatalf("daemon err missing")
	}
}
