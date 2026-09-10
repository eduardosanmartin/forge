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
		{Role: "assistant", Content: "mira:\n```python\nprint('hola mundo con un token larguisimo " + strings.Repeat("y", 120) + "')\n```\nlisto"},
		{IsTool: true, ToolName: "fs_read", Meta: strings.Repeat("y", 100)},
		{Role: "tool", ToolName: "fs_list", Content: "<<TOOL_RESULT:fs_list>>\n<CONTENT>\n" + `[{"name":"a","is_dir":true,"size":0},{"name":"b.txt","is_dir":false,"size":1234}]` + "\n</CONTENT>\n</TOOL_RESULT:fs_list>"},
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
	// User bubble: YOU label + rounded box corners.
	if !strings.Contains(out, "YOU") {
		t.Fatalf("user bubble should carry a YOU label, got %q", out)
	}
	for _, corner := range []string{"╭", "╮", "╰", "╯"} {
		if !strings.Contains(out, corner) {
			t.Fatalf("user bubble should be a rounded box, missing %q", corner)
		}
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

func TestBuildContentFsListPretty(t *testing.T) {
	pal := testPalette()
	raw := "<<TOOL_RESULT:fs_list>>\n<CONTENT>\n" + `[{"name":".atl","is_dir":true,"size":0},{"name":"go.mod","is_dir":false,"size":1846}]` + "\n</CONTENT>\n</TOOL_RESULT:fs_list>"
	entries := []Entry{{Role: "tool", ToolName: "fs_list", Content: raw}}
	out := BuildContent(entries, pal, 80)
	for _, want := range []string{"[dir]", ".atl/", "[file]", "go.mod", "1,846 bytes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty fs_list should contain %q, got %q", want, out)
		}
	}
	for _, ugly := range []string{"<<TOOL_RESULT", "<CONTENT>", "is_dir", "mod_time"} {
		if strings.Contains(out, ugly) {
			t.Fatalf("pretty fs_list should not leak transport fencing, got %q", out)
		}
	}
}

func TestBuildContentSubResponseSeparators(t *testing.T) {
	pal := testPalette()
	entries := []Entry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling"},
		{IsTool: true, ToolName: "fs_list"},
		{Role: "tool", ToolName: "fs_list", Content: "ok"},
		{Role: "assistant", Content: "done here"},
	}
	out := BuildContent(entries, pal, 80)
	if !strings.Contains(out, strings.Repeat("\u2500", 8)) {
		t.Fatalf("sub-response boundary should render a faint rule, got %q", out)
	}
}

func TestBuildContentCodeBlockTint(t *testing.T) {
	pal := testPalette()
	entries := []Entry{{Role: "assistant", Content: "mira:\n```python\nprint(1)\n```\nlisto"}}
	out := BuildContent(entries, pal, 80)
	if strings.Contains(out, "```") {
		t.Fatalf("fences should be consumed, got %q", out)
	}
	if !strings.Contains(out, "48;2;30;25;21") {
		t.Fatalf("code should carry elevated-bg SGR, got %q", out)
	}
	if !strings.Contains(out, "python") || !strings.Contains(out, "print(1)") || !strings.Contains(out, "listo") {
		t.Fatalf("code content lost, got %q", out)
	}
}

func TestBuildContentSummaryGreen(t *testing.T) {
	pal := testPalette()
	entries := []Entry{{Role: "assistant", Content: "done", Meta: "tokens 2708 \u00b7 model \u00b7 7.4s", Summary: true}}
	out := BuildContent(entries, pal, 80)
	if !strings.Contains(out, "38;2;138;166;114") {
		t.Fatalf("summary meta should carry success-green SGR, got %q", out)
	}
	plain := Entry{Role: "assistant", Content: "done", Meta: "tokens 10"}
	outPlain := BuildContent([]Entry{plain}, pal, 80)
	if strings.Contains(outPlain, "38;2;138;166;114") {
		t.Fatalf("plain meta must stay dim, got %q", outPlain)
	}
}
