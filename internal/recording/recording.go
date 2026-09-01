// Package recording implements replay presentation for forge sessions.
//
// RNF-6.2: the persistent message store IS the recording. Since v0 the store
// persists full tool calls (tool_calls JSON on assistant messages) and tool
// results (role="tool" messages). This package adds replay presentation and
// the success marker, and delegates mining to internal/mining.
package recording

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/store"
)

const maxContentPreview = 300

// FormatReplay renders messages as grouped turns for replay.
//
// Each user message starts a turn; assistant messages with their tool_calls
// (name + redacted args JSON) follow; tool messages (redacted content,
// truncated) are shown inline. Usage totals per turn and a grand total are
// appended. redact is applied to displayed args and tool results; if nil it is
// treated as identity. Output is plain text (no color deps).
func FormatReplay(messages []store.Message, redact func(string) string) string {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	if len(messages) == 0 {
		return "no messages\n"
	}

	// Messages from store.GetMessages are newest-first (DESC). For replay we
	// want chronological (oldest-first). GetMessagesSince is already ASC.
	// Detect order: if first message has higher Seq than last, reverse.
	ordered := make([]store.Message, len(messages))
	copy(ordered, messages)
	if len(ordered) > 1 && ordered[0].Seq > ordered[len(ordered)-1].Seq {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}

	var b strings.Builder
	turnIdx := 0
	// Track usage per turn.
	type usage struct {
		prompt, completion, total int
	}
	var usages []usage
	var cur usage
	var grand usage
	inTurn := false

	flushTurnUsage := func() {
		if inTurn && (cur.prompt != 0 || cur.completion != 0 || cur.total != 0) {
			usages = append(usages, cur)
			grand.prompt += cur.prompt
			grand.completion += cur.completion
			grand.total += cur.total
			cur = usage{}
		} else if inTurn {
			usages = append(usages, cur)
			cur = usage{}
		}
	}

	for _, msg := range ordered {
		switch msg.Role {
		case "user":
			// Flush previous turn usage if any.
			if inTurn {
				flushTurnUsage()
			}
			turnIdx++
			inTurn = true
			// Start fresh usage for new turn.
			cur = usage{}
			b.WriteString(fmt.Sprintf("=== Turn %d ===\n", turnIdx))
			content := redact(msg.Content)
			if len(content) > maxContentPreview {
				content = content[:maxContentPreview] + "..."
			}
			b.WriteString(fmt.Sprintf("user: %s\n", content))
		case "assistant":
			if !inTurn {
				turnIdx++
				b.WriteString(fmt.Sprintf("=== Turn %d ===\n", turnIdx))
				inTurn = true
				cur = usage{}
			}
			if msg.Content != "" {
				content := redact(msg.Content)
				if len(content) > maxContentPreview {
					content = content[:maxContentPreview] + "..."
				}
				b.WriteString(fmt.Sprintf("assistant: %s\n", content))
			}
			for _, tc := range msg.ToolCalls {
				args := tc.Function.Arguments
				if args != "" {
					args = redact(args)
					// Try to pretty-compact JSON if valid.
					var raw json.RawMessage
					if json.Valid([]byte(args)) {
						// Keep as is, but ensure redacted version is shown.
					} else {
						_ = raw
					}
					if len(args) > maxContentPreview {
						args = args[:maxContentPreview] + "..."
					}
				}
				b.WriteString(fmt.Sprintf("  tool_call: %s(%s)\n", tc.Function.Name, args))
			}
			if msg.Usage != nil {
				cur.prompt += msg.Usage.PromptTokens
				cur.completion += msg.Usage.CompletionTokens
				cur.total += msg.Usage.TotalTokens
			}
		case "tool":
			// Tool results belong to the current turn's assistant calls.
			content := redact(msg.Content)
			if len(content) > maxContentPreview {
				content = content[:maxContentPreview] + "..."
			}
			// Collapse newlines for single-line preview.
			content = strings.ReplaceAll(content, "\n", " ")
			content = strings.ReplaceAll(content, "\r", " ")
			name := msg.Name
			if name == "" {
				name = "tool"
			}
			b.WriteString(fmt.Sprintf("  tool_result [%s]: %s\n", name, content))
		default:
			content := redact(msg.Content)
			if len(content) > maxContentPreview {
				content = content[:maxContentPreview] + "..."
			}
			b.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, content))
		}
	}
	// Flush last turn.
	if inTurn {
		flushTurnUsage()
	}

	// Per-turn usage summary.
	if len(usages) > 0 {
		hasUsage := false
		for _, u := range usages {
			if u.total != 0 || u.prompt != 0 || u.completion != 0 {
				hasUsage = true
				break
			}
		}
		if hasUsage {
			b.WriteString("\n--- Usage per turn ---\n")
			for i, u := range usages {
				if u.total != 0 || u.prompt != 0 {
					b.WriteString(fmt.Sprintf("turn %d: prompt=%d completion=%d total=%d\n", i+1, u.prompt, u.completion, u.total))
				} else {
					b.WriteString(fmt.Sprintf("turn %d: no usage\n", i+1))
				}
			}
			b.WriteString(fmt.Sprintf("grand total: prompt=%d completion=%d total=%d\n", grand.prompt, grand.completion, grand.total))
		}
	}

	return b.String()
}
