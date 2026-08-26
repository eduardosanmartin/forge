// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"context"
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
type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
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
