package tui

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

func seededModel(layout string, showSidebar bool) Model {
	cfg := TUIConfig{Layout: layout, Palette: "ember", Sidebar: showSidebar}
	pal := MustGetPalette("ember")
	m := NewModel(cfg, pal, "ember", ".forge/config.json", nil)
	m.SetSize(80, 24)
	m.sessionID = "sess-test12345678"
	m.daemonAddr = "127.0.0.1:8765"
	m.daemonVers = "v0.0.0-dev"
	m.sessions = []daemon.SessionResult{
		{ID: "sess-test12345678", MessageCount: 3},
		{ID: "sess-other87654321", MessageCount: 1},
	}
	m.entries = []components.Entry{
		{Role: "user", Content: "Hello, forge!"},
		{Role: "assistant", Content: "Hello! How can I help?", Meta: "tokens 12"},
		{IsTool: true, ToolName: "fs_read"},
		{Role: "assistant", Content: "Here is the file content."},
	}
	m.rebuildTranscript()
	m.input.SetValue("follow-up message")
	return m
}

func TestGoldenViewHybrid(t *testing.T) {
	m := seededModel(LayoutHybrid, true)
	view := m.View().Content
	golden.RequireEqual(t, view)
}

func TestGoldenViewHybridNoSidebar(t *testing.T) {
	m := seededModel(LayoutHybrid, false)
	view := m.View().Content
	golden.RequireEqual(t, view)
}

func TestGoldenViewSession(t *testing.T) {
	m := seededModel(LayoutSession, true)
	view := m.View().Content
	golden.RequireEqual(t, view)
}

func TestGoldenViewMinimal(t *testing.T) {
	m := seededModel(LayoutMinimal, false)
	view := m.View().Content
	golden.RequireEqual(t, view)
}

func TestGoldenViewMinimalWithOverlay(t *testing.T) {
	m := seededModel(LayoutMinimal, true)
	view := m.View().Content
	golden.RequireEqual(t, view)
}
