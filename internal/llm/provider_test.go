// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"encoding/json"
	"testing"

	"github.com/eduardosanmartin/forge/internal/logging"
)

func TestMessage_JSONRoundtrip(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: "Hello",
		ToolCalls: []ToolCall{{
			ID:   "call_123",
			Type: "function",
			Function: ToolCallFunction{
				Name:      "test_func",
				Arguments: `{"arg": "value"}`,
			},
		}},
		ToolCallID: "call_123",
		Name:       "test_func",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Role != msg.Role {
		t.Errorf("role: got %q, want %q", decoded.Role, msg.Role)
	}
	if decoded.Content != msg.Content {
		t.Errorf("content: got %q, want %q", decoded.Content, msg.Content)
	}
	if len(decoded.ToolCalls) != len(msg.ToolCalls) {
		t.Errorf("tool_calls count: got %d, want %d", len(decoded.ToolCalls), len(msg.ToolCalls))
	}
	if decoded.ToolCallID != msg.ToolCallID {
		t.Errorf("tool_call_id: got %q, want %q", decoded.ToolCallID, msg.ToolCallID)
	}
	if decoded.Name != msg.Name {
		t.Errorf("name: got %q, want %q", decoded.Name, msg.Name)
	}
}

func TestChatRequest_JSONRoundtrip(t *testing.T) {
	temp := 0.7
	maxTokens := 100
	req := ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Tools: []ToolDef{{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "test_func",
				Description: "A test function",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"arg": map[string]any{"type": "string"},
					},
				},
			},
		}},
		ToolChoice:  "auto",
		Temperature: &temp,
		MaxTokens:   &maxTokens,
		Stream:      true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ChatRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Model != req.Model {
		t.Errorf("model: got %q, want %q", decoded.Model, req.Model)
	}
	if len(decoded.Messages) != len(req.Messages) {
		t.Errorf("messages count: got %d, want %d", len(decoded.Messages), len(req.Messages))
	}
	if len(decoded.Tools) != len(req.Tools) {
		t.Errorf("tools count: got %d, want %d", len(decoded.Tools), len(req.Tools))
	}
	if decoded.ToolChoice != req.ToolChoice {
		t.Errorf("tool_choice: got %q, want %q", decoded.ToolChoice, req.ToolChoice)
	}
	if decoded.Temperature == nil || *decoded.Temperature != *req.Temperature {
		t.Errorf("temperature: got %v, want %v", decoded.Temperature, req.Temperature)
	}
	if decoded.MaxTokens == nil || *decoded.MaxTokens != *req.MaxTokens {
		t.Errorf("max_tokens: got %v, want %v", decoded.MaxTokens, req.MaxTokens)
	}
	if decoded.Stream != req.Stream {
		t.Errorf("stream: got %v, want %v", decoded.Stream, req.Stream)
	}
}

func TestStreamChunk_JSONRoundtrip(t *testing.T) {
	finishReason := "stop"
	chunk := StreamChunk{
		ID:    "chatcmpl-123",
		Model: "test-model",
		Choices: []StreamChoice{{
			Index: 0,
			Delta: Message{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: &finishReason,
		}},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded StreamChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != chunk.ID {
		t.Errorf("id: got %q, want %q", decoded.ID, chunk.ID)
	}
	if len(decoded.Choices) != len(chunk.Choices) {
		t.Errorf("choices count: got %d, want %d", len(decoded.Choices), len(chunk.Choices))
	}
	if decoded.Choices[0].Delta.Content != chunk.Choices[0].Delta.Content {
		t.Errorf("delta content: got %q, want %q", decoded.Choices[0].Delta.Content, chunk.Choices[0].Delta.Content)
	}
	if decoded.Choices[0].FinishReason == nil || *decoded.Choices[0].FinishReason != *chunk.Choices[0].FinishReason {
		t.Errorf("finish_reason: got %v, want %v", decoded.Choices[0].FinishReason, chunk.Choices[0].FinishReason)
	}
}

func TestChatResponse_JSONRoundtrip(t *testing.T) {
	resp := ChatResponse{
		ID:    "chatcmpl-123",
		Model: "test-model",
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: "Hello"},
			FinishReason: "stop",
		}},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != resp.ID {
		t.Errorf("id: got %q, want %q", decoded.ID, resp.ID)
	}
	if len(decoded.Choices) != len(resp.Choices) {
		t.Errorf("choices count: got %d, want %d", len(decoded.Choices), len(resp.Choices))
	}
	if decoded.Usage == nil {
		t.Fatal("usage is nil")
	}
	if decoded.Usage.PromptTokens != resp.Usage.PromptTokens {
		t.Errorf("prompt_tokens: got %d, want %d", decoded.Usage.PromptTokens, resp.Usage.PromptTokens)
	}
	if decoded.Usage.CompletionTokens != resp.Usage.CompletionTokens {
		t.Errorf("completion_tokens: got %d, want %d", decoded.Usage.CompletionTokens, resp.Usage.CompletionTokens)
	}
	if decoded.Usage.TotalTokens != resp.Usage.TotalTokens {
		t.Errorf("total_tokens: got %d, want %d", decoded.Usage.TotalTokens, resp.Usage.TotalTokens)
	}
}

func TestToolCall_JSONRoundtrip(t *testing.T) {
	tc := ToolCall{
		ID:   "call_123",
		Type: "function",
		Function: ToolCallFunction{
			Name:      "get_weather",
			Arguments: `{"location": "San Francisco"}`,
		},
	}

	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ToolCall
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != tc.ID {
		t.Errorf("id: got %q, want %q", decoded.ID, tc.ID)
	}
	if decoded.Type != tc.Type {
		t.Errorf("type: got %q, want %q", decoded.Type, tc.Type)
	}
	if decoded.Function.Name != tc.Function.Name {
		t.Errorf("function.name: got %q, want %q", decoded.Function.Name, tc.Function.Name)
	}
	if decoded.Function.Arguments != tc.Function.Arguments {
		t.Errorf("function.arguments: got %q, want %q", decoded.Function.Arguments, tc.Function.Arguments)
	}
}

// TestLoggingRedaction verifies that logging.Redact works on common secret patterns
// used in LLM requests/responses.
func TestLoggingRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "OpenAI API key",
			input:    `Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz`,
			expected: `Authorization: Bearer [REDACTED]`,
		},
		{
			name:     "API key in JSON",
			input:    `{"api_key": "sk-abcdefghijklmnopqrstuvwxyz"}`,
			expected: `{"api_key": "[REDACTED]"}`,
		},
		{
			name:     "Bearer token",
			input:    `Bearer ghp_abcdefghijklmnopqrstuvwxyz123456`,
			expected: `Bearer [REDACTED]`,
		},
		{
			name:     "Password in URL",
			input:    `https://user:password123@example.com`,
			expected: `https://user:[REDACTED]@example.com`,
		},
		{
			name:     "No secrets",
			input:    `{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`,
			expected: `{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := logging.Redact(tt.input)
			if result != tt.expected {
				t.Errorf("Redact() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestStreamChoice_JSONRoundtrip verifies StreamChoice serializes correctly.
func TestStreamChoice_JSONRoundtrip(t *testing.T) {
	finishReason := "tool_calls"
	sc := StreamChoice{
		Index: 0,
		Delta: Message{
			Role:      "assistant",
			Content:   "",
			ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "func", Arguments: `{}`}}},
		},
		FinishReason: &finishReason,
	}

	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded StreamChoice
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Index != sc.Index {
		t.Errorf("index: got %d, want %d", decoded.Index, sc.Index)
	}
	if len(decoded.Delta.ToolCalls) != len(sc.Delta.ToolCalls) {
		t.Errorf("tool_calls count: got %d, want %d", len(decoded.Delta.ToolCalls), len(sc.Delta.ToolCalls))
	}
	if decoded.FinishReason == nil || *decoded.FinishReason != *sc.FinishReason {
		t.Errorf("finish_reason: got %v, want %v", decoded.FinishReason, sc.FinishReason)
	}
}
