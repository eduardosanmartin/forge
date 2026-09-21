// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter and a model registry supporting hot-swap.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// Message represents a single message in a chat conversation.
// JSON tags cover both directions: requests serialize through
// buildRequestBody today, but responses decode directly into these
// structs, so every wire-facing field MUST carry its OpenAI-compatible
// snake_case tag.
type Message struct {
	Role       string     `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant->tool
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool->result
	Name       string     `json:"name,omitempty"`         // tool name for tool messages
	// Reasoning captures a reasoning-capable model's "thinking" text when the
	// provider sends it as a field separate from Content (observed live from
	// OpenRouter: delta.reasoning, alongside delta.content). Without this
	// field json.Unmarshal silently drops it. A model that spends its whole
	// completion-token budget reasoning and never transitions to a final
	// answer leaves Content empty but Reasoning populated — the agent loop's
	// final-response step (internal/agent/loop.go) falls back to showing
	// this instead of a blank reply. Reused for both the streaming delta
	// (StreamChoice.Delta) and the non-streaming Choice.Message, since both
	// share this struct.
	Reasoning string `json:"reasoning,omitempty"`
}

// ToolCall represents a function call made by the assistant.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and JSON-encoded arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ChatRequest represents a request to the chat completion API.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef // from tools.Registry.List() → JSONSchema
	ToolChoice  string    // "auto" | "none" | specific
	Temperature *float64
	MaxTokens   *int
	Stream      bool
	SessionID   string // stable conversation ID, sent as x-opencode-session when non-empty (required by opencode go provider)
}

// ToolDef represents a tool definition for function calling.
type ToolDef struct {
	Type     string // "function"
	Function ToolFunctionDef
}

// ToolFunctionDef holds the function metadata and JSON Schema parameters.
type ToolFunctionDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// ChatResponse represents a non-streaming chat completion response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk represents a single chunk in a streaming response.
//
// Extension (WU3, additive, backward compatible): Usage and Error are optional.
// Usage is populated on the final chunk when the provider supplies token counts.
// Error is populated when the provider signals a mid-stream error (e.g. Anthropic `error` event);
// downstream consumers should treat a chunk with Error != "" as terminal and fail the turn.
// Mid-stream failures fail the turn predictably; the next turn may use non-streaming fallback
// if streaming is disabled. No goroutine is left blocked on context cancellation.
type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// UnmarshalJSON gives StreamChunk a custom decoder because OpenAI-compatible
// providers are inconsistent about the wire shape of a mid-stream error: most
// send a plain string, but OpenRouter relaying an upstream provider failure
// sends a structured object instead — confirmed live (hojaDeRuta-embeddings-skills.md
// verification on macOS): a real 503 ("Upstream error from Nvidia: Service
// temporarily overloaded") arrived as
// {"error":{"code":503,"message":"...","metadata":{...}}}. With the plain
// `Error string` field decoded via the default json.Unmarshal, that object
// shape fails to decode — and because Go's json.Unmarshal fails the WHOLE
// struct on one bad field, not just that field, the entire chunk (including
// any real content in the same line) was silently dropped, logged only at
// DEBUG, with the turn returning no content and no visible error at all.
// This decodes Error permissively: plain string first, then an object's
// "message" (plus its "code" if present), then the raw JSON as a last
// resort — so a real upstream error always ends up in the caller-visible
// StreamChunk.Error string, never swallowed.
func (c *StreamChunk) UnmarshalJSON(data []byte) error {
	type streamChunkAlias StreamChunk // avoid recursing into this method
	var raw struct {
		streamChunkAlias
		Error json.RawMessage `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*c = StreamChunk(raw.streamChunkAlias)
	c.Error = decodeStreamChunkError(raw.Error)
	return nil
}

// decodeStreamChunkError parses a stream chunk's "error" field permissively
// — see StreamChunk.UnmarshalJSON's doc comment for why. Returns "" for an
// absent/empty field.
func decodeStreamChunkError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Message != "" {
		if obj.Code != 0 {
			return fmt.Sprintf("%s (code %d)", obj.Message, obj.Code)
		}
		return obj.Message
	}
	return string(raw)
}

// StreamChoice represents a choice delta in a streaming chunk.
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

// Provider is the interface that all LLM providers must implement.
type Provider interface {
	// Chat sends a non-streaming chat completion request.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ChatStream sends a streaming chat completion request.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
	// ListModels returns the list of model names available from this provider.
	ListModels() ([]string, error)
	// Close releases any resources held by the provider.
	Close() error
}
