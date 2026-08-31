// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"strings"

	"github.com/eduardosanmartin/forge/internal/logging"
)

// fenceTag converts a tool name to its fence tag.
// e.g., "fs_read" -> "fs_read", "shell_exec" -> "shell_exec"
func fenceTag(name string) string {
	return strings.ReplaceAll(name, ":", ".")
}

// fenceOpen returns the opening fence for a tool name.
func fenceOpen(name string) string {
	return "<<TOOL_RESULT:" + fenceTag(name) + ">>"
}

// fenceClose returns the closing fence for a tool name.
func fenceClose(name string) string {
	return "</TOOL_RESULT:" + fenceTag(name) + ">"
}

// Fence wraps content in the tool result fence.
// If the content already contains the closing fence sequence (extremely unlikely),
// the content is escaped by doubling the opening '<' of the fence markers in the content.
// This means any occurrence of "<<TOOL_RESULT:name>>" becomes "<<<TOOL_RESULT:name>>"
// and "</TOOL_RESULT:name>" becomes "<</TOOL_RESULT:name>>" in the content,
// so the decoder can distinguish them from the real fences.
func Fence(name, content string) string {
	closeFence := fenceClose(name)
	openFence := fenceOpen(name)

	// Escape any fence-like sequences in the content by doubling the leading '<'
	escapedContent := content
	// Escape opening fence pattern: <<TOOL_RESULT:name>> -> <<<TOOL_RESULT:name>>
	escapedContent = strings.ReplaceAll(escapedContent, openFence, "<"+openFence)
	// Escape closing fence pattern: </TOOL_RESULT:name> -> <</TOOL_RESULT:name>>
	escapedContent = strings.ReplaceAll(escapedContent, closeFence, "<"+closeFence+">")

	return openFence + "\n<CONTENT>\n" + escapedContent + "\n</CONTENT>\n" + closeFence
}

// RedactAndFence applies Redact to the content and then wraps it in a fence.
// This is the uniform operation applied by Registry.Execute to every tool result.
func RedactAndFence(name, content string) string {
	redacted := logging.Redact(content)
	return Fence(name, redacted)
}

// RedactMetadata applies Redact to all string values in metadata.
func RedactMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch typed := v.(type) {
	case string:
		return logging.Redact(typed)
	case []string:
		out := make([]string, len(typed))
		for i, s := range typed {
			out[i] = logging.Redact(s)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			out[k] = logging.Redact(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = redactValue(v)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = redactValue(v)
		}
		return out
	default:
		return v
	}
}
