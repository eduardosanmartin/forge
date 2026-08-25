// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

func TestNewOllamaProvider_AllowlistDeny(t *testing.T) {
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	defer func() { _ = logger }()

	// Empty allowlist should deny all
	_, err := NewOllamaProvider("http://127.0.0.1:11434/v1", []string{}, logger)
	if err == nil {
		t.Fatal("expected error for empty allowlist, got nil")
	}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}

	// Host not in allowlist
	_, err = NewOllamaProvider("http://127.0.0.1:11434/v1", []string{"localhost"}, logger)
	if err == nil {
		t.Fatal("expected error for host not in allowlist, got nil")
	}

	// Exact match with port
	_, err = NewOllamaProvider("http://127.0.0.1:11434/v1", []string{"127.0.0.1:11434"}, logger)
	if err != nil {
		t.Fatalf("unexpected error for exact match: %v", err)
	}
}

func TestNewOllamaProvider_AllowlistExactMatch(t *testing.T) {
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	defer func() { _ = logger }()

	tests := []struct {
		name      string
		baseURL   string
		allowlist []string
		wantError bool
	}{
		{
			name:      "exact host:port match",
			baseURL:   "http://127.0.0.1:11434/v1",
			allowlist: []string{"127.0.0.1:11434"},
			wantError: false,
		},
		{
			name:      "localhost with port",
			baseURL:   "http://localhost:11434/v1",
			allowlist: []string{"localhost:11434"},
			wantError: false,
		},
		{
			name:      "different port denied",
			baseURL:   "http://127.0.0.1:11434/v1",
			allowlist: []string{"127.0.0.1:11435"},
			wantError: true,
		},
		{
			name:      "hostname vs IP denied",
			baseURL:   "http://localhost:11434/v1",
			allowlist: []string{"127.0.0.1:11434"},
			wantError: true,
		},
		{
			name:      "https allowed",
			baseURL:   "https://api.example.com/v1",
			allowlist: []string{"api.example.com"},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _, _ := logging.New(logging.Config{Level: "error"})
			_, err := NewOllamaProvider(tt.baseURL, tt.allowlist, logger)
			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestOllamaProvider_Chat_NonStreaming(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{
			ID:           "chatcmpl-test",
			Model:        "test-model",
			Content:      "Hello, world!",
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Stream:   false,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.ID != "chatcmpl-test" {
		t.Errorf("ID: got %q, want %q", resp.ID, "chatcmpl-test")
	}
	if len(resp.Choices) != 1 {
		t.Errorf("choices count: got %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("content: got %q, want %q", resp.Choices[0].Message.Content, "Hello, world!")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage: got %v, want total=15", resp.Usage)
	}

	// Verify request was made correctly
	req := mock.LastRequest()
	if req == nil {
		t.Fatal("no request captured")
	}
	if req.Method != http.MethodPost {
		t.Errorf("method: got %q, want %q", req.Method, http.MethodPost)
	}
	if req.Path != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q", req.Path, "/v1/chat/completions")
	}
}

func TestOllamaProvider_Chat_WithTools(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{
			ID:           "chatcmpl-test",
			Model:        "test-model",
			Content:      "",
			FinishReason: "tool_calls",
			ToolCalls: []ToolCall{{
				ID:   "call_123",
				Type: "function",
				Function: ToolCallFunction{
					Name:      "get_weather",
					Arguments: `{"location": "San Francisco"}`,
				},
			}},
		}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "What's the weather?"}},
		Tools: []ToolDef{{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
			},
		}},
		ToolChoice: "auto",
		Stream:     false,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool name: got %q, want %q", tc.Function.Name, "get_weather")
	}

	// Verify request included tools
	req := mock.LastRequest()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Errorf("tools not sent correctly: %v", body["tools"])
	}
	toolChoice := body["tool_choice"]
	if toolChoice != "auto" {
		t.Errorf("tool_choice: got %v, want auto", toolChoice)
	}
}

func TestOllamaProvider_ChatStream(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&StreamChunkBuilder{
			ID:    "chatcmpl-stream",
			Model: "test-model",
			Deltas: []StreamDelta{
				{Content: "Hello"},
				{Content: ", "},
				{Content: "world!"},
				{Content: "", FinishReason: stringPtr("stop")},
			},
		}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(10 * time.Second)
	defer cancel()

	ch, err := provider.ChatStream(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 4 {
		t.Errorf("chunks count: got %d, want 4", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("chunk 0: got %q, want %q", chunks[0].Choices[0].Delta.Content, "Hello")
	}
	if chunks[3].Choices[0].FinishReason == nil || *chunks[3].Choices[0].FinishReason != "stop" {
		t.Errorf("final chunk finish_reason: got %v, want stop", chunks[3].Choices[0].FinishReason)
	}
}

func TestOllamaProvider_ChatStream_WithToolCalls(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&StreamChunkBuilder{
			ID:    "chatcmpl-stream",
			Model: "test-model",
			Deltas: []StreamDelta{
				{Content: ""},
				{
					Content: "",
					ToolCalls: []ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: ToolCallFunction{
							Name:      "get_weather",
							Arguments: `{"location": "SF"}`,
						},
					}},
				},
				{Content: "", FinishReason: stringPtr("tool_calls")},
			},
		}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(10 * time.Second)
	defer cancel()

	ch, err := provider.ChatStream(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Weather?"}},
		Tools:    []ToolDef{{Type: "function", Function: ToolFunctionDef{Name: "get_weather", Parameters: map[string]any{}}}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var toolCalls []ToolCall
	for chunk := range ch {
		for _, tc := range chunk.Choices[0].Delta.ToolCalls {
			toolCalls = append(toolCalls, tc)
		}
	}

	if len(toolCalls) != 1 {
		t.Errorf("tool calls collected: got %d, want 1", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool name: got %q, want %q", toolCalls[0].Function.Name, "get_weather")
	}
}

func TestOllamaProvider_ListModels_Merge(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	// /models returns OpenAI compat models
	mock.SetHandler("/v1/models", func(req *http.Request) *MockResponse {
		return (&ModelsResponseBuilder{Data: []string{"model-a", "model-b", "model-c"}}).Build()
	})

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	// Wait a bit for background model fetch
	time.Sleep(100 * time.Millisecond)

	models, err := provider.ListModels()
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	// Should have all models from the single endpoint
	expected := map[string]bool{"model-a": true, "model-b": true, "model-c": true}
	if len(models) != 3 {
		t.Errorf("model count: got %d, want 3; models=%v", len(models), models)
	}
	for _, m := range models {
		if !expected[m] {
			t.Errorf("unexpected model: %q", m)
		}
	}
}

func TestOllamaProvider_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "404 model not found",
			statusCode: http.StatusNotFound,
			body:       `{"error": {"message": "model not found"}}`,
			wantErr:    "model not found (404)",
		},
		{
			name:       "429 rate limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error": {"message": "rate limit"}}`,
			wantErr:    "rate limited (429)",
		},
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"error": {"message": "invalid api key"}}`,
			wantErr:    "unauthorized (401)",
		},
		{
			name:       "500 server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error": {"message": "internal error"}}`,
			wantErr:    "server error (500)",
		},
		{
			name:       "503 service unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error": {"message": "overloaded"}}`,
			wantErr:    "upstream unavailable (503)",
		},
		{
			name:       "400 bad request",
			statusCode: http.StatusBadRequest,
			body:       `{"error": {"message": "invalid request"}}`,
			wantErr:    "bad request (400)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockServer()
			defer mock.Close()

			mock.SetDefaultResponse(
				(&ErrorResponseBuilder{
					StatusCode: tt.statusCode,
					Message:    "test error",
				}).Build(),
			)

			logger, _, _ := logging.New(logging.Config{Level: "error"})
			provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			defer provider.Close()

			ctx, cancel := ContextWithTimeout(5 * time.Second)
			defer cancel()

			_, err = provider.Chat(ctx, ChatRequest{
				Model:    "test-model",
				Messages: []Message{{Role: "user", Content: "Hello"}},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error: got %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestOllamaProvider_ContextCancellation(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	// Slow response
	mock.SetDefaultResponse(&MockResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"test","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"slow"},"finish_reason":"stop"}]}`),
		Delay:      2 * time.Second,
	})

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(100 * time.Millisecond)
	defer cancel()

	_, err = provider.Chat(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !contains(err.Error(), "timeout") && !contains(err.Error(), "deadline") && !contains(err.Error(), "cancel") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestOllamaProvider_Close(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{ID: "test", Model: "test", Content: "ok", FinishReason: "stop"}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	if err := provider.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Second close should be no-op
	if err := provider.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// Operations after close should fail
	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()
	_, err = provider.Chat(ctx, ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Error("expected error after close, got nil")
	}
}

func TestOllamaProvider_SSEParsing_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		wantChunks int
	}{
		{
			name: "empty lines skipped",
			lines: []string{
				"",
				`{"id":"1","choices":[{"index":0,"delta":{"content":"a"}}]}`,
				"",
				`{"id":"2","choices":[{"index":0,"delta":{"content":"b"}}]}`,
			},
			wantChunks: 2,
		},
		{
			name: "data: prefix with spaces",
			lines: []string{
				"{\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}",
				"  {\"id\":\"2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"b\"}}]}",
			},
			wantChunks: 2,
		},
		{
			name: "DONE terminates",
			lines: []string{
				`{"id":"1","choices":[{"index":0,"delta":{"content":"a"}}]}`,
				"[DONE]",
				`{"id":"2","choices":[{"index":0,"delta":{"content":"b"}}]}`,
			},
			wantChunks: 1,
		},
		{
			name: "malformed JSON skipped",
			lines: []string{
				`{"id":"1","choices":[{"index":0,"delta":{"content":"a"}}]}`,
				"not json",
				`{"id":"2","choices":[{"index":0,"delta":{"content":"b"}}]}`,
			},
			wantChunks: 2,
		},
		{
			name: "data: [DONE] with prefix",
			lines: []string{
				`{"id":"1","choices":[{"index":0,"delta":{"content":"a"}}]}`,
				"[DONE]",
			},
			wantChunks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockServer()
			defer mock.Close()

			mock.SetDefaultResponse(&MockResponse{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       []byte(strings.Join(tt.lines, "\n")),
				Stream:     true,
			})

			logger, _, _ := logging.New(logging.Config{Level: "error"})
			provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			defer provider.Close()

			ctx, cancel := ContextWithTimeout(10 * time.Second)
			defer cancel()

			ch, err := provider.ChatStream(ctx, ChatRequest{Model: "test", Messages: []Message{{Role: "user", Content: "hi"}}, Stream: true})
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}

			count := 0
			for range ch {
				count++
			}
			if count != tt.wantChunks {
				t.Errorf("chunk count: got %d, want %d", count, tt.wantChunks)
			}
		})
	}
}

func TestOllamaProvider_TemperatureAndMaxTokens(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{ID: "test", Model: "test", Content: "ok", FinishReason: "stop"}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	temp := 0.5
	maxTokens := 200
	_, err = provider.Chat(ctx, ChatRequest{
		Model:       "test-model",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
		Stream:      false,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	req := mock.LastRequest()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)

	if body["temperature"] != 0.5 {
		t.Errorf("temperature: got %v, want 0.5", body["temperature"])
	}
	if body["max_tokens"] != float64(200) {
		t.Errorf("max_tokens: got %v, want 200", body["max_tokens"])
	}
}

func TestOllamaProvider_Chat_NilOptionalFields(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{ID: "test", Model: "test", Content: "ok", FinishReason: "stop"}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOllamaProvider(mock.URL(), []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	// All optional fields nil
	_, err = provider.Chat(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	req := mock.LastRequest()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)

	// Should not have temperature, max_tokens, tools, tool_choice
	if _, ok := body["temperature"]; ok {
		t.Error("temperature should not be in request when nil")
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("max_tokens should not be in request when nil")
	}
	if _, ok := body["tools"]; ok {
		t.Error("tools should not be in request when empty")
	}
	if _, ok := body["tool_choice"]; ok {
		t.Error("tool_choice should not be in request when empty")
	}
}

func hostFromURL(u string) string {
	// Extract host:port from URL
	// Simple extraction for test purposes
	if len(u) > 7 { // http://
		u = u[7:]
	} else if len(u) > 8 { // https://
		u = u[8:]
	}
	if idx := indexSlash(u); idx >= 0 {
		u = u[:idx]
	}
	return u
}

func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func stringPtr(s string) *string {
	return &s
}
