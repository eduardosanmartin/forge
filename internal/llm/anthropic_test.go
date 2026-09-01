package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/logging"
)

func TestAnthropicProvider_Chat_WireFormat(t *testing.T) {
	// Capture request body and return canned Anthropic response.
	var capturedBody map[string]any
	var capturedHeaders http.Header

	rawResp := []byte(`{
		"id": "msg_anthro_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{"type": "text", "text": "Hello from Anthropic"},
			{"type": "tool_use", "id": "toolu_1", "name": "fs_read", "input": {"path": "main.go"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 120, "output_tokens": 34}
	}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawResp)
	}))
	defer srv.Close()

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewAnthropicProvider(srv.URL, "test-key", []string{hostFromURL(srv.URL)}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	maxTokens := 512
	temp := 0.7
	req := ChatRequest{
		Model: "claude-3-5-sonnet-20241022",
		Messages: []Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "system", Content: "Second system prompt"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "toolu_1", Type: "function", Function: ToolCallFunction{Name: "fs_read", Arguments: `{"path":"main.go"}`}}}},
			{Role: "tool", Content: "file content here", ToolCallID: "toolu_1", Name: "fs_read"},
		},
		Tools: []ToolDef{{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "fs_read",
				Description: "Read file",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			},
		}},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	}
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	resp, err := provider.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Request assertions.
	if capturedBody["model"] != "claude-3-5-sonnet-20241022" {
		t.Errorf("model: got %v", capturedBody["model"])
	}
	if capturedBody["max_tokens"] != float64(512) {
		t.Errorf("max_tokens: got %v", capturedBody["max_tokens"])
	}
	if capturedHeaders.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key header: got %q", capturedHeaders.Get("x-api-key"))
	}
	if capturedHeaders.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("anthropic-version header: got %q", capturedHeaders.Get("anthropic-version"))
	}
	// System extraction.
	if capturedBody["system"] != "You are helpful\n\nSecond system prompt" {
		t.Errorf("system: got %q", capturedBody["system"])
	}
	// Tools mapping: input_schema
	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: got %v", capturedBody["tools"])
	}
	toolMap := tools[0].(map[string]any)
	if toolMap["name"] != "fs_read" {
		t.Errorf("tool name: got %v", toolMap["name"])
	}
	if _, ok := toolMap["input_schema"]; !ok {
		t.Errorf("tool input_schema missing")
	}
	// Messages: system should be omitted, tool result wrapped as user with tool_result.
	msgs, ok := capturedBody["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing")
	}
	// Expect 3 messages: user, assistant with tool_use, user with tool_result (system omitted)
	if len(msgs) != 3 {
		t.Fatalf("messages count: got %d, want 3", len(msgs))
	}
	// Check assistant tool_use mapping.
	assistant := msgs[1].(map[string]any)
	assistantContent := assistant["content"].([]any)
	foundToolUse := false
	for _, c := range assistantContent {
		m := c.(map[string]any)
		if m["type"] == "tool_use" && m["name"] == "fs_read" {
			foundToolUse = true
			if m["id"] != "toolu_1" {
				t.Errorf("tool_use id: got %v", m["id"])
			}
		}
	}
	if !foundToolUse {
		t.Errorf("assistant tool_use not found")
	}
	// Check tool result wrapping.
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "user" {
		t.Errorf("tool message role: got %v", toolMsg["role"])
	}
	toolContent := toolMsg["content"].([]any)[0].(map[string]any)
	if toolContent["type"] != "tool_result" {
		t.Errorf("tool_result type: got %v", toolContent["type"])
	}
	if toolContent["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_use_id: got %v", toolContent["tool_use_id"])
	}

	// Response mapping assertions.
	if resp.ID != "msg_anthro_123" {
		t.Errorf("ID: got %q", resp.ID)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices: %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello from Anthropic" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls: %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "fs_read" || tc.ID != "toolu_1" {
		t.Errorf("tool call: %+v", tc)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	if args["path"] != "main.go" {
		t.Errorf("args path: %v", args["path"])
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason: got %q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 34 || resp.Usage.TotalTokens != 154 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestAnthropicProvider_Chat_DefaultMaxTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	// No MaxTokens set -> should default to 4096
	_, _ = p.Chat(ctx, ChatRequest{Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}})
	if capturedBody["max_tokens"] != float64(4096) {
		t.Errorf("default max_tokens: got %v, want 4096", capturedBody["max_tokens"])
	}
}

func TestAnthropicProvider_ChatStream_NotSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	_, err := p.ChatStream(nil, ChatRequest{Model: "claude-3"})
	if !errors.Is(err, ErrStreamingNotSupported) {
		t.Fatalf("expected ErrStreamingNotSupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error should mention anthropic, got %q", err.Error())
	}
}

func TestAnthropicProvider_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			if r.Header.Get("x-api-key") == "" {
				t.Error("x-api-key header missing on ListModels")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5-sonnet-20241022"},{"id":"claude-3-opus-20240229"}],"has_more":false}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, err := NewAnthropicProvider(srv.URL, "k", []string{hostFromURL(srv.URL)}, logger)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer p.Close()
	models, err := p.ListModels()
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "claude-3-5-sonnet-20241022" || models[1] != "claude-3-opus-20240229" {
		t.Errorf("models: %v", models)
	}
}

func TestAnthropicProvider_ErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	_, err := p.Chat(ctx, ChatRequest{Model: "claude-3", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limited error, got %v", err)
	}
}

func TestAnthropicProvider_Allowlist(t *testing.T) {
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	_, err := NewAnthropicProvider("http://127.0.0.1:11434", "", []string{"other-host"}, logger)
	if err == nil {
		t.Fatal("expected allowlist deny")
	}
}

func TestAnthropicProvider_DefaultBaseURL(t *testing.T) {
	// Empty baseURL should default to https://api.anthropic.com but we cannot hit real endpoint.
	// Instead we test that empty baseURL with allowlist containing api.anthropic.com succeeds construction.
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	// Use allowlist that matches default host, but avoid actual network fetch by using invalid host? The provider will try to fetch models at startup and warn, not fail.
	// We test construction succeeds.
	_, err := NewAnthropicProvider("", "key", []string{"api.anthropic.com"}, logger)
	if err != nil {
		t.Fatalf("expected default baseURL to be allowed, got %v", err)
	}
}
