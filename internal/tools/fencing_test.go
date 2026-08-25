// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"strings"
	"testing"
)

// TestFenceTag tests fence tag conversion.
func TestFenceTag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"fs.read", "fs.read"},
		{"fs.write", "fs.write"},
		{"fs.list", "fs.list"},
		{"shell.exec", "shell.exec"},
		{"git", "git"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := fenceTag(tc.input)
			if result != tc.expected {
				t.Errorf("fenceTag(%q) = %q, expected %q", tc.input, result, tc.expected)
			}
		})
	}
}

// TestFenceOpenClose tests open and close fence generation.
func TestFenceOpenClose(t *testing.T) {
	open := fenceOpen("fs.read")
	close := fenceClose("fs.read")

	if open != "<<TOOL_RESULT:fs.read>>" {
		t.Errorf("Unexpected open fence: %s", open)
	}
	if close != "</TOOL_RESULT:fs.read>" {
		t.Errorf("Unexpected close fence: %s", close)
	}
}

// TestFence_Basic tests basic fencing.
func TestFence_Basic(t *testing.T) {
	content := "test content"
	fenced := Fence("fs.read", content)

	if !strings.HasPrefix(fenced, "<<TOOL_RESULT:fs.read>>") {
		t.Errorf("Missing open fence: %s", fenced[:min(50, len(fenced))])
	}
	if !strings.HasSuffix(fenced, "</TOOL_RESULT:fs.read>") {
		t.Errorf("Missing close fence: %s", fenced[max(0, len(fenced)-50):])
	}
	if !strings.Contains(fenced, "<CONTENT>") {
		t.Error("Missing CONTENT tag")
	}
	if !strings.Contains(fenced, "</CONTENT>") {
		t.Error("Missing closing CONTENT tag")
	}
	if !strings.Contains(fenced, content) {
		t.Errorf("Content not found in fenced output: %s", fenced)
	}
}

// TestFence_EmptyContent tests fencing empty content.
func TestFence_EmptyContent(t *testing.T) {
	fenced := Fence("fs.read", "")

	// Empty content produces <CONTENT>\n\n</CONTENT> (empty line between tags)
	if !strings.Contains(fenced, "<CONTENT>\n\n</CONTENT>") {
		t.Errorf("Empty content should have empty CONTENT tags: %s", fenced)
	}
}

// TestFence_EscapeClosingFence tests escaping when content contains fence sequences.
func TestFence_EscapeClosingFence(t *testing.T) {
	// Content that contains the closing fence sequence
	content := "some text </TOOL_RESULT:fs.read> more text"
	fenced := Fence("fs.read", content)

	// The closing fence in content should be escaped to <</TOOL_RESULT:fs.read>>
	// The real closing fence appears once at the end
	if !strings.Contains(fenced, "<</TOOL_RESULT:fs.read>>") {
		t.Errorf("Closing fence in content should be escaped: %s", fenced)
	}
	// Real closing fence at the end
	if !strings.HasSuffix(fenced, "</TOOL_RESULT:fs.read>") {
		t.Errorf("Should end with real closing fence: %s", fenced)
	}

	// Content with opening fence should also be escaped
	contentWithOpenFence := "some text <<TOOL_RESULT:fs.read>> more text"
	fenced2 := Fence("fs.read", contentWithOpenFence)
	if !strings.Contains(fenced2, "<<<TOOL_RESULT:fs.read>>") {
		t.Errorf("Opening fence in content should be escaped: %s", fenced2)
	}
	// And should still have exactly one real closing fence at the end
	if !strings.HasSuffix(fenced2, "</TOOL_RESULT:fs.read>") {
		t.Errorf("Should end with real closing fence: %s", fenced2)
	}
}

// TestRedactAndFence tests combined redaction and fencing.
func TestRedactAndFence(t *testing.T) {
	content := "api_key=sk-1234567890abcdef"
	fenced := RedactAndFence("fs.read", content)

	if strings.Contains(fenced, "sk-1234567890abcdef") {
		t.Errorf("Secret should be redacted: %s", fenced)
	}
	if !strings.Contains(fenced, "[REDACTED]") {
		t.Errorf("Should contain [REDACTED]: %s", fenced)
	}
	if !strings.HasPrefix(fenced, "<<TOOL_RESULT:fs.read>>") {
		t.Error("Should be fenced")
	}
}

// TestRedactMetadata_String tests metadata string redaction.
func TestRedactMetadata_String(t *testing.T) {
	metadata := map[string]any{
		"plain":  "value",
		"secret": "api_key=sk-1234567890abcdef",
	}

	redacted := RedactMetadata(metadata)

	if redacted["plain"] != "value" {
		t.Error("Plain value should be unchanged")
	}
	if strings.Contains(redacted["secret"].(string), "sk-1234567890abcdef") {
		t.Error("Secret should be redacted")
	}
	if !strings.Contains(redacted["secret"].(string), "[REDACTED]") {
		t.Error("Should contain [REDACTED]")
	}
}

// TestRedactMetadata_NestedMap tests nested map redaction.
func TestRedactMetadata_NestedMap(t *testing.T) {
	metadata := map[string]any{
		"nested": map[string]any{
			"token": "ghp_1234567890abcdefghij",
			"plain": "value",
		},
	}

	redacted := RedactMetadata(metadata)

	nested := redacted["nested"].(map[string]any)
	if strings.Contains(nested["token"].(string), "ghp_") {
		t.Error("Nested secret should be redacted")
	}
	if nested["plain"] != "value" {
		t.Error("Nested plain value should be unchanged")
	}
}

// TestRedactMetadata_List tests list redaction.
func TestRedactMetadata_List(t *testing.T) {
	metadata := map[string]any{
		"list": []any{"plain", "token=ghp_abcdefghijklmnopqrst", 123},
	}

	redacted := RedactMetadata(metadata)

	list := redacted["list"].([]any)
	if list[0] != "plain" {
		t.Error("List plain value should be unchanged")
	}
	if strings.Contains(list[1].(string), "ghp_") {
		t.Error("List secret should be redacted")
	}
	if list[2] != 123 {
		t.Error("Non-string values should be unchanged")
	}
}

// TestRedactMetadata_Nil tests nil metadata.
func TestRedactMetadata_Nil(t *testing.T) {
	result := RedactMetadata(nil)
	if result != nil {
		t.Error("Nil input should return nil")
	}
}

// TestRedactMetadata_Empty tests empty metadata.
func TestRedactMetadata_Empty(t *testing.T) {
	result := RedactMetadata(map[string]any{})
	if result == nil {
		t.Error("Empty map should return empty map, not nil")
	}
	if len(result) != 0 {
		t.Error("Empty map should remain empty")
	}
}

// TestRedactValue_NonString tests that non-string values are preserved.
func TestRedactValue_NonString(t *testing.T) {
	testCases := []struct {
		name  string
		value any
		check func(any) bool
	}{
		{"int", 123, func(v any) bool { i, ok := v.(int); return ok && i == 123 }},
		{"float", 12.5, func(v any) bool { f, ok := v.(float64); return ok && f == 12.5 }},
		{"bool", true, func(v any) bool { b, ok := v.(bool); return ok && b == true }},
		{"map", map[string]int{"a": 1}, func(v any) bool {
			m, ok := v.(map[string]int)
			return ok && m["a"] == 1
		}},
		{"slice", []int{1, 2, 3}, func(v any) bool {
			s, ok := v.([]int)
			return ok && len(s) == 3 && s[0] == 1 && s[1] == 2 && s[2] == 3
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := redactValue(tc.value)
			if !tc.check(result) {
				t.Errorf("Non-string value %T should be unchanged, got %v", tc.value, result)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
