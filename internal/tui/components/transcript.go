package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

// Entry is a single transcript entry for rendering.
type Entry struct {
	Role     string
	Content  string
	Meta     string // dim meta line (model/duration)
	ToolName string // if tool call compact line
	IsTool   bool
	Seq      int  // daemon message seq; used by callers to dedup fetched history
	Local    bool // true for the optimistic local echo before daemon confirmation
}

// TranscriptModel wraps a viewport for the transcript area.
type TranscriptModel struct {
	Viewport viewport.Model
	Entries  []Entry
	Palette  Palette
	Width    int
	Height   int
}

// NewTranscript creates a transcript model with the given palette and size.
func NewTranscript(pal Palette, w, h int) TranscriptModel {
	vp := viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))
	return TranscriptModel{Viewport: vp, Palette: pal, Width: w, Height: h}
}

// SetSize updates viewport dimensions.
func (m *TranscriptModel) SetSize(w, h int) {
	m.Width = w
	m.Height = h
	m.Viewport.SetWidth(w)
	m.Viewport.SetHeight(h)
	m.Viewport.SetContent(BuildContent(m.Entries, m.Palette, w))
}

// SetPalette updates the palette and rebuilds content.
func (m *TranscriptModel) SetPalette(p Palette) {
	m.Palette = p
	m.Viewport.SetContent(BuildContent(m.Entries, m.Palette, m.Width))
}

// Append adds an entry and refreshes viewport content.
func (m *TranscriptModel) Append(e Entry) {
	m.Entries = append(m.Entries, e)
	m.Viewport.SetContent(BuildContent(m.Entries, m.Palette, m.Width))
	m.Viewport.GotoBottom()
}

// SetEntries replaces entries wholesale.
func (m *TranscriptModel) SetEntries(entries []Entry) {
	m.Entries = entries
	m.Viewport.SetContent(BuildContent(m.Entries, m.Palette, m.Width))
	m.Viewport.GotoBottom()
}

// BuildContent is a pure function that renders entries into a string using the
// palette tokens. User messages get an accent left-border block.
func BuildContent(entries []Entry, pal Palette, width int) string {
	if len(entries) == 0 {
		return pal.FaintStyle().Render("No messages yet. Type a prompt to begin.")
	}
	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		switch {
		case e.IsTool:
			line := fmt.Sprintf("⏺ %s", e.ToolName)
			if e.Meta != "" {
				line += " · " + e.Meta
			}
			sb.WriteString(pal.DimStyle().Render(line))
		case e.Role == "user":
			// Accent left-border block.
			block := pal.AccentStyle().Render("▎ ") + pal.TextStyle().Render(e.Content)
			sb.WriteString(block)
		case e.Role == "assistant":
			sb.WriteString(pal.TextStyle().Render(e.Content))
			if e.Meta != "" {
				sb.WriteString("\n")
				sb.WriteString(pal.DimStyle().Render(e.Meta))
			}
		case e.Role == "tool":
			sb.WriteString(pal.DimStyle().Render(fmt.Sprintf("⏺ %s · %s", e.ToolName, e.Content)))
		default:
			sb.WriteString(pal.TextStyle().Render(fmt.Sprintf("%s: %s", e.Role, e.Content)))
		}
	}
	return sb.String()
}

// EntriesFromMessages converts daemon MessageResults into transcript entries.
// Tool info is extracted from MessageResult.ToolCalls and ExecuteTurnResult.ToolTrace.
// For TUI-1 we render role+content only if tool info is not trivially
// available; this helper handles both cases.
func EntriesFromMessages(msgs []daemon.MessageResult, toolTrace []daemon.ToolTraceResult) []Entry {
	var out []Entry
	// Build trace lookup for durations etc — currently trace has Name+OK but no duration
	// so we render compact lines for tool calls embedded in assistant messages.
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, Entry{Role: "user", Content: m.Content, Seq: m.Seq})
		case "assistant":
			meta := ""
			if m.Usage != nil {
				meta = fmt.Sprintf("tokens %d", m.Usage.TotalTokens)
			}
			// If tool calls present, render them as compact lines after assistant content.
			content := m.Content
			if len(m.ToolCalls) > 0 {
				// If content empty but tool calls exist, show tool names.
				if strings.TrimSpace(content) == "" {
					content = "(tool calls)"
				}
			} else if strings.TrimSpace(content) == "" {
				content = "(empty)"
			}
			out = append(out, Entry{Role: "assistant", Content: content, Meta: meta, Seq: m.Seq})
			for _, tc := range m.ToolCalls {
				out = append(out, Entry{IsTool: true, ToolName: tc.Function.Name, Seq: m.Seq})
			}
		case "tool":
			name := m.Name
			if name == "" {
				name = "tool"
			}
			out = append(out, Entry{Role: "tool", ToolName: name, Content: m.Content, Seq: m.Seq})
		default:
			out = append(out, Entry{Role: m.Role, Content: m.Content, Seq: m.Seq})
		}
	}
	// Also append toolTrace compact lines if not already covered by ToolCalls.
	// In daemon handler, ToolTrace duplicates ToolCalls; we avoid double-rendering
	// by checking if msgs already produced tool entries. For simplicity, if toolTrace
	// non-empty and no tool entries yet, append them.
	hasToolEntries := false
	for _, e := range out {
		if e.IsTool {
			hasToolEntries = true
			break
		}
	}
	if !hasToolEntries && len(toolTrace) > 0 {
		for _, tc := range toolTrace {
			out = append(out, Entry{IsTool: true, ToolName: tc.Name})
		}
	}
	return out
}
