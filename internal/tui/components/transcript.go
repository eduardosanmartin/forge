package components

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

// Entry is a single transcript entry for rendering.
type Entry struct {
	Role      string
	Content   string
	Meta      string // dim meta line (model/duration)
	Summary   bool   // turn-summary Meta renders in success green instead of dim
	ToolName  string // if tool call compact line
	IsTool    bool
	Seq       int  // daemon message seq; used by callers to dedup fetched history
	Local     bool // true for the optimistic local echo before daemon confirmation
	Streaming bool // true for in-flight streaming preview; renders with caret
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

// wrapPlain word-wraps plain text to width, breaking long tokens/URLs.
// Uses lipgloss.Wrap which hard-wraps words exceeding the limit.
func wrapPlain(s string, width int) string {
	if width <= 0 {
		return s
	}
	if width < 10 {
		width = 10
	}
	return lipgloss.Wrap(s, width, "")
}

// BuildContent is a pure function that renders entries into a string using the
// palette tokens. User messages get an accent left-border block.
// All content is word-wrapped to width (accounting for the 2-rune accent prefix
// on user entries) and long tokens/URLs are hard-broken so no line exceeds width.
func BuildContent(entries []Entry, pal Palette, width int) string {
	if len(entries) == 0 {
		msg := "No messages yet. Type a prompt to begin."
		if width > 0 {
			msg = wrapPlain(msg, width)
		}
		return pal.FaintStyle().Render(msg)
	}
	if width <= 0 {
		width = 80
	}
	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		// Visual blocks: a user turn always opens one; an assistant reply
		// following tool traffic opens a sub-response block. A faint rule
		// delimits them so nested tool iterations stay readable.
		if i > 0 && startsVisualBlock(e, entries[i-1]) {
			sb.WriteString(pal.FaintStyle().Render(strings.Repeat("─", width)))
			sb.WriteString("\n")
		}
		switch {
		case e.IsTool:
			line := fmt.Sprintf("⏺ %s", e.ToolName)
			if e.Meta != "" {
				line += " · " + e.Meta
			}
			wrapped := wrapPlain(line, toolAvail(width))
			wLines := strings.Split(wrapped, "\n")
			for li, wl := range wLines {
				if li > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("  " + pal.DimStyle().Render(wl))
			}
		case e.Role == "user":
			// User bubble (readability): rounded box with accent border,
			// elevated background and a "YOU" label, so sent messages are
			// unmistakable next to assistant replies. Box Width includes
			// the borders, so inner text wraps to width-4 (2 border +
			// 2 padding) and no rendered line exceeds width.
			avail := width - 4
			if avail < 10 {
				avail = 10
			}
			var inner strings.Builder
			inner.WriteString(pal.AccentStyle().Bold(true).Render("YOU"))
			plainWrapped := wrapPlain(e.Content, avail)
			for _, ln := range strings.Split(plainWrapped, "\n") {
				inner.WriteString("\n")
				inner.WriteString(pal.TextStyle().Render(ln))
			}
			if e.Meta != "" {
				metaWrapped := wrapPlain(e.Meta, avail)
				for _, ml := range strings.Split(metaWrapped, "\n") {
					inner.WriteString("\n")
					inner.WriteString(pal.DimStyle().Render(ml))
				}
			}
			box := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(pal.Accent)).
				Background(lipgloss.Color(pal.BGElevated)).
				Width(width).
				Padding(0, 1)
			sb.WriteString(box.Render(inner.String()))
		case e.Role == "assistant":
			// Reserve 1 column for the streaming caret so the last wrapped
			// line never exceeds width when the caret is appended.
			avail := width
			if e.Streaming {
				avail = width - 1
				if avail < 10 {
					avail = 10
				}
			}
			// e.Streaming entries mutate on every delta (~14fps, see
			// handleMessageDelta), so caching them would never hit and would
			// just leak one cache entry per intermediate partial state —
			// only a finished message's rendering (content now immutable)
			// is worth memoizing. This is the actual fix for a real
			// slowdown/hang reported live in `forge tui`: BuildContent
			// re-renders the WHOLE transcript on every streaming tick, and
			// chroma's per-token tokenization is far more expensive than the
			// old flat-tint styling it replaced — without caching, every
			// historical code block/table got fully re-highlighted ~14
			// times a second for the entire duration of any later reply.
			lines := renderAssistantLines(e.Content, avail, pal, !e.Streaming)
			if e.Streaming && len(lines) > 0 {
				lines[len(lines)-1] += pal.AccentStyle().Render("▌")
			}
			for li, ln := range lines {
				if li > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(ln)
			}
			if e.Meta != "" {
				sb.WriteString("\n")
				metaWrapped := wrapPlain(e.Meta, width)
				metaStyle := pal.DimStyle()
				if e.Summary {
					metaStyle = pal.SuccessStyle()
				}
				mLines := strings.Split(metaWrapped, "\n")
				for mi, ml := range mLines {
					if mi > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(metaStyle.Render(ml))
				}
			}
		case e.Role == "tool":
			sb.WriteString("  " + pal.DimStyle().Render(fmt.Sprintf("⏺ %s", e.ToolName)))
			if body := humanizeToolResult(e.ToolName, e.Content); body != "" {
				wrapped := wrapPlain(body, toolAvail(width))
				for _, wl := range strings.Split(wrapped, "\n") {
					sb.WriteString("\n")
					sb.WriteString("  " + pal.DimStyle().Render(wl))
				}
			}
		default:
			line := fmt.Sprintf("%s: %s", e.Role, e.Content)
			wrapped := wrapPlain(line, width)
			wLines := strings.Split(wrapped, "\n")
			for li, wl := range wLines {
				if li > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(pal.TextStyle().Render(wl))
			}
		}
	}
	return sb.String()
}

// toolAvail is the wrap width for indented tool lines (2-space indent).
func toolAvail(width int) int {
	avail := width - 2
	if avail < 10 {
		avail = 10
	}
	return avail
}

// startsVisualBlock reports whether entry e opens a new visual block: a user
// turn always does; an assistant reply following tool traffic opens a
// sub-response block inside a multi-iteration turn.
func startsVisualBlock(e, prev Entry) bool {
	if e.Role == "user" {
		return true
	}
	if e.Role == "assistant" && !e.IsTool && (prev.IsTool || prev.Role == "tool") {
		return true
	}
	return false
}

// humanizeToolResult renders a tool result entry for humans: strips the
// <<TOOL_RESULT>>/<CONTENT> transport fencing, and pretty-prints known
// shapes (fs_list JSON array → clean file list) instead of raw dumps.
// The store keeps the raw fenced content; this is presentation only.
func humanizeToolResult(toolName, content string) string {
	inner := content
	if s := strings.Index(inner, "<CONTENT>"); s >= 0 {
		inner = inner[s+len("<CONTENT>"):]
		if e := strings.Index(inner, "</CONTENT>"); e >= 0 {
			inner = inner[:e]
		}
	}
	inner = strings.TrimSpace(inner)
	if toolName == "fs_list" {
		if pretty, ok := prettyFsList(inner); ok {
			return pretty
		}
	}
	return inner
}

// fsListEntry mirrors one element of the fs_list JSON array.
type fsListEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// prettyFsList renders an fs_list JSON array as a clean file list.
func prettyFsList(inner string) (string, bool) {
	var items []fsListEntry
	if err := json.Unmarshal([]byte(inner), &items); err != nil {
		return "", false
	}
	if len(items) == 0 {
		return "(empty directory)", true
	}
	var sb strings.Builder
	for i, it := range items {
		if i > 0 {
			sb.WriteString("\n")
		}
		if it.IsDir {
			sb.WriteString("[dir]  " + it.Name + "/")
		} else {
			sb.WriteString(fmt.Sprintf("[file] %s (%s bytes)", it.Name, formatTokens(int(it.Size))))
		}
	}
	return sb.String(), true
}

// styledLines wraps text and styles every line with st.
func styledLines(wrapped string, st lipgloss.Style) []string {
	lines := strings.Split(wrapped, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, st.Render(ln))
	}
	return out
}

// assistantRenderCache memoizes renderAssistantLinesCompute's (content,
// avail, pal) -> rendered lines, so BuildContent's per-tick re-render of the
// WHOLE transcript (every ~70ms while any message streams — see
// scheduleDeltaRebuild) doesn't re-run chroma tokenization/table layout for
// every historical message that hasn't changed. Only cacheable=true calls
// (finished messages) touch it; a streaming message's content changes every
// tick and would never hit anyway.
var (
	assistantRenderCache  sync.Map // assistantRenderKey -> []string
	assistantRenderCacheN atomic.Int64
)

// assistantRenderCacheMax bounds memory over a very long session (many
// distinct completed messages, or many terminal resizes changing avail).
// Crude eviction — drop the whole cache and start over — rather than a real
// LRU: hitting this cap at all should be rare, so the simplest correct fix
// beats a more precise one that's more code to get right.
const assistantRenderCacheMax = 500

type assistantRenderKey struct {
	content string
	avail   int
	pal     Palette
}

// renderAssistantLines renders assistant text: syntax-highlighting fenced
// code blocks (chroma) with a line-number gutter so code reads as code, and
// box-drawing any GFM tables in the surrounding prose so columns stay
// aligned instead of wrapping mid-row. Fences are consumed; an opening-fence
// language tag renders as a dim label line above its block. cacheable should
// be false for an in-progress streaming entry (see the comment at the call
// site) and true for a finished message.
func renderAssistantLines(content string, avail int, pal Palette, cacheable bool) []string {
	if !cacheable {
		return renderAssistantLinesCompute(content, avail, pal)
	}
	key := assistantRenderKey{content: content, avail: avail, pal: pal}
	if cached, ok := assistantRenderCache.Load(key); ok {
		return cached.([]string)
	}
	lines := renderAssistantLinesCompute(content, avail, pal)
	if assistantRenderCacheN.Load() >= assistantRenderCacheMax {
		assistantRenderCache.Range(func(k, _ any) bool {
			assistantRenderCache.Delete(k)
			return true
		})
		assistantRenderCacheN.Store(0)
	}
	if _, loaded := assistantRenderCache.LoadOrStore(key, lines); !loaded {
		assistantRenderCacheN.Add(1)
	}
	return lines
}

func renderAssistantLinesCompute(content string, avail int, pal Palette) []string {
	if !strings.Contains(content, "```") {
		return renderProseWithTables(content, avail, pal)
	}
	var out []string
	parts := strings.Split(content, "```")
	for i, part := range parts {
		if i%2 == 0 {
			// Prose around blocks; pure-whitespace parts are just fence
			// newlines and add nothing.
			if strings.TrimSpace(part) == "" {
				continue
			}
			out = append(out, renderProseWithTables(part, avail, pal)...)
			continue
		}
		lang := ""
		body := part
		if idx := strings.Index(part, "\n"); idx >= 0 {
			if tag := strings.TrimSpace(part[:idx]); tag != "" && !strings.Contains(tag, " ") && len([]rune(tag)) <= 20 {
				lang = tag
				body = part[idx+1:]
			}
		}
		if lang != "" {
			out = append(out, pal.DimStyle().Render(lang))
		}
		body = strings.Trim(body, "\n")
		out = append(out, renderHighlightedCodeBlock(lang, body, avail, pal)...)
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
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
