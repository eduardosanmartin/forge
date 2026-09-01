package recording

import (
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
)

func TestFormatReplay_Table(t *testing.T) {
	tests := []struct {
		name     string
		messages []store.Message
		redact   func(string) string
		contains []string
	}{
		{
			name:     "empty session",
			messages: nil,
			contains: []string{"no messages"},
		},
		{
			name: "single turn grouping",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: "hello"},
				{Seq: 2, Role: "assistant", Content: "hi there"},
			},
			contains: []string{"=== Turn 1 ===", "user: hello", "assistant: hi there"},
		},
		{
			name: "tool call rendering",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: "read file"},
				{Seq: 2, Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"/tmp/foo"}`}}}},
				{Seq: 3, Role: "tool", ToolCallID: "c1", Name: "fs_read", Content: "file content"},
			},
			contains: []string{"tool_call: fs_read", "tool_result [fs_read]", "file content"},
		},
		{
			name: "redaction applied to args and results",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: "do secret"},
				{Seq: 2, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Function: llm.ToolCallFunction{Name: "fs_write", Arguments: `{"token":"ghp_12345678901234567890"}`}}}},
				{Seq: 3, Role: "tool", ToolCallID: "c1", Name: "fs_write", Content: "saved token ghp_12345678901234567890"},
			},
			redact: func(s string) string {
				if strings.Contains(s, "ghp_") {
					return strings.ReplaceAll(s, "ghp_12345678901234567890", "[REDACTED]")
				}
				return s
			},
			contains: []string{"[REDACTED]", "tool_call: fs_write", "tool_result"},
		},
		{
			name: "usage totals per turn and grand total",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: "hi"},
				{Seq: 2, Role: "assistant", Content: "hello", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
				{Seq: 3, Role: "user", Content: "next"},
				{Seq: 4, Role: "assistant", Content: "bye", Usage: &llm.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}},
			},
			contains: []string{"turn 1: prompt=10", "turn 2: prompt=20", "grand total: prompt=30"},
		},
		{
			name: "truncation of long content",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: strings.Repeat("a", 500)},
				{Seq: 2, Role: "assistant", Content: strings.Repeat("b", 500)},
			},
			contains: []string{"..."},
		},
		{
			name: "multiple turns grouping",
			messages: []store.Message{
				{Seq: 1, Role: "user", Content: "first"},
				{Seq: 2, Role: "assistant", Content: "answer1"},
				{Seq: 3, Role: "user", Content: "second"},
				{Seq: 4, Role: "assistant", Content: "answer2"},
			},
			contains: []string{"=== Turn 1 ===", "=== Turn 2 ===", "first", "second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatReplay(tt.messages, tt.redact)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("FormatReplay missing %q in output:\n%s", want, got)
				}
			}
			if tt.name == "single turn grouping" && strings.Contains(got, "=== Turn 2 ===") {
				t.Fatalf("unexpected second turn in single turn test: %s", got)
			}
		})
	}
}

func TestFormatReplay_DescendingInputReversed(t *testing.T) {
	// Simulate store.GetMessages which returns DESC order.
	msgsDesc := []store.Message{
		{Seq: 4, Role: "assistant", Content: "bye"},
		{Seq: 3, Role: "user", Content: "second"},
		{Seq: 2, Role: "assistant", Content: "hi"},
		{Seq: 1, Role: "user", Content: "first"},
	}
	got := FormatReplay(msgsDesc, nil)
	// Should render chronological: first before second.
	idxFirst := strings.Index(got, "first")
	idxSecond := strings.Index(got, "second")
	if idxFirst == -1 || idxSecond == -1 {
		t.Fatalf("missing content in replay: %s", got)
	}
	if idxFirst > idxSecond {
		t.Fatalf("messages not reversed to chronological: first idx %d > second idx %d\n%s", idxFirst, idxSecond, got)
	}
}

func TestFormatReplay_EmptyContentHandling(t *testing.T) {
	msgs := []store.Message{
		{Seq: 1, Role: "user", Content: "prompt"},
		{Seq: 2, Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{ID: "1", Function: llm.ToolCallFunction{Name: "shell_exec", Arguments: `{"cmd":"echo hi"}`}}}},
	}
	got := FormatReplay(msgs, nil)
	if !strings.Contains(got, "shell_exec") {
		t.Fatalf("should render tool call even with empty assistant content: %s", got)
	}
}
