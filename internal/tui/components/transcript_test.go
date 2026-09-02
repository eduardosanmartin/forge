package components

import (
	"regexp"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// Regression (TUI-4, orchestrator): no rendered line may exceed the given
// width after stripping ANSI — overflowing lines break the panel borders and
// bleed into the sidebar (owner's manual test block 1).
func TestBuildContentWrapNeverExceedsWidth(t *testing.T) {
	pal := testPalette()
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	longURL := "https://example.com/aaaaaaaaaa/bbbbbbbbbb/cccccccccc/dddddddddd"
	entries := []Entry{
		{Role: "user", Content: "un mensaje bastante largo que deberia envolverse en varias lineas " + strings.Repeat("x", 120)},
		{Role: "assistant", Content: strings.Repeat("palabra ", 40) + longURL},
		{Role: "assistant", Content: strings.Repeat("streaming ", 30), Streaming: true},
		{IsTool: true, ToolName: "fs_read", Meta: strings.Repeat("y", 100)},
	}
	for _, width := range []int{20, 40, 80, 120} {
		out := BuildContent(entries, pal, width)
		for i, line := range strings.Split(out, "\n") {
			plain := ansi.ReplaceAllString(line, "")
			if got := len([]rune(plain)); got > width {
				t.Fatalf("width %d: line %d has %d runes > %d (%q)", width, i, got, width, plain)
			}
		}
	}
}

func testPalette() Palette {
	return Palette{
		BG: "#161210", BGElevated: "#1e1915", Border: "#332b24", Text: "#e8e2da",
		Dim: "#8a8178", Faint: "#5c554e", Accent: "#d97757",
		Success: "#8aa672", Warning: "#d9a257", Error: "#c4544d",
	}
}

func TestBuildContentEmpty(t *testing.T) {
	pal := testPalette()
	out := BuildContent(nil, pal, 80)
	if !strings.Contains(out, "No messages") {
		t.Fatalf("empty should show placeholder, got %q", out)
	}
}

func TestBuildContentUserAccentBorder(t *testing.T) {
	pal := testPalette()
	entries := []Entry{{Role: "user", Content: "hello"}}
	out := BuildContent(entries, pal, 80)
	if !strings.Contains(out, "hello") {
		t.Fatalf("missing content")
	}
	// Accent SGR should be present (truecolor for #d97757)
	if !strings.Contains(out, "38;2;217;119;87") {
		// fallback via direct ansi check already covered in palette test; but transcript should also contain accent.
		t.Fatalf("user block should contain accent SGR, got %q", out)
	}
}

func TestBuildContentAssistantMeta(t *testing.T) {
	pal := testPalette()
	entries := []Entry{{Role: "assistant", Content: "world", Meta: "tokens 10"}}
	out := BuildContent(entries, pal, 80)
	if !strings.Contains(out, "world") || !strings.Contains(out, "tokens 10") {
		t.Fatalf("assistant render missing, got %q", out)
	}
}

func TestBuildContentToolCompact(t *testing.T) {
	pal := testPalette()
	entries := []Entry{{IsTool: true, ToolName: "fs_read", Meta: ""}}
	out := BuildContent(entries, pal, 80)
	if !strings.Contains(out, "fs_read") || !strings.Contains(out, "⏺") {
		t.Fatalf("tool compact missing, got %q", out)
	}
}

func TestTranscriptAppendAndViewport(t *testing.T) {
	pal := testPalette()
	m := NewTranscript(pal, 80, 10)
	if len(m.Entries) != 0 {
		t.Fatal("initial not empty")
	}
	m.Append(Entry{Role: "user", Content: "hi"})
	if len(m.Entries) != 1 {
		t.Fatalf("append len %d", len(m.Entries))
	}
	content := m.Viewport.GetContent()
	if !strings.Contains(content, "hi") {
		t.Fatalf("viewport content missing hi %q", content)
	}
	m.SetEntries([]Entry{{Role: "assistant", Content: "bye"}})
	if len(m.Entries) != 1 || m.Entries[0].Content != "bye" {
		t.Fatalf("SetEntries failed")
	}
}

func TestEntriesFromMessages(t *testing.T) {
	msgs := []daemon.MessageResult{
		{Role: "user", Content: "hi", Seq: 1},
		{Role: "assistant", Content: "hello", Seq: 2, Usage: &daemon.UsageResult{TotalTokens: 5}, ToolCalls: []daemon.ToolCallResult{
			{ID: "1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "fs_read", Arguments: `{"path":"a"}`}},
		}},
		{Role: "tool", Content: "ok", Name: "fs_read", Seq: 3},
	}
	trace := []daemon.ToolTraceResult{{Name: "fs_read", OK: true}}
	entries := EntriesFromMessages(msgs, trace)
	if len(entries) < 3 {
		t.Fatalf("entries len %d", len(entries))
	}
	// First should be user
	if entries[0].Role != "user" || entries[0].Content != "hi" {
		t.Fatalf("first entry %+v", entries[0])
	}
	// Should contain tool compact
	foundTool := false
	for _, e := range entries {
		if e.IsTool && e.ToolName == "fs_read" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("tool entry missing %+v", entries)
	}
}

func TestEntriesFromMessagesEmptyContent(t *testing.T) {
	msgs := []daemon.MessageResult{{Role: "assistant", Content: "", Seq: 1}}
	entries := EntriesFromMessages(msgs, nil)
	if entries[0].Content != "(empty)" {
		t.Fatalf("empty assistant should be (empty) got %q", entries[0].Content)
	}
}
