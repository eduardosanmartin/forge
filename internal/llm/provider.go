// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"context"
)

// Message represents a single message in a chat conversation.
type Message struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []ToolCall // assistant->tool
	ToolCallID string     // tool->result
	Name       string     // tool name for tool messages
}

// ToolCall represents a function call made by the assistant.
type ToolCall struct {
	ID       string
	Type     string // "function"
	Function ToolCallFunction
}

// ToolCallFunction holds the function name and JSON-encoded arguments.
type ToolCallFunction struct {
	Name      string
	Arguments string // JSON string
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
	ID      string
	Model   string
	Choices []Choice
	Usage   *Usage
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int
	Message      Message
	FinishReason string
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StreamChunk represents a single chunk in a streaming response.
type StreamChunk struct {
	ID      string
	Model   string
	Choices []StreamChoice
}

// StreamChoice represents a choice delta in a streaming chunk.
type StreamChoice struct {
	Index        int
	Delta        Message
	FinishReason *string
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
