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
	m.Version = "v0.0.0-dev"
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

func TestFooterDaemonErr(t *testing.T) {
	pal := testPalette()
	m := NewFooter(pal, 80)
	m.DaemonErr = "daemon unreachable"
	content := m.Render()
	if !strings.Contains(content, "daemon unreachable") {
		t.Fatalf("daemon err missing")
	}
}
