package llm

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

// TestOpenAICompatibleProvider_Chat_RealWireFormat is a regression lock: the mock
// server normally round-trips Go structs (Go-dialect JSON), which masked a
// bug where response structs lacked snake_case json tags — tool_calls,
// finish_reason, and usage silently decoded to zero values against real
// servers. This test feeds RAW OpenAI-compatible wire bytes and asserts
// every critical field decodes.
func TestOpenAICompatibleProvider_Chat_RealWireFormat(t *testing.T) {
	raw := []byte(`{
		"id": "chatcmpl-wire",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "qwen2.5-coder:7b",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "fs_read",
						"arguments": "{\"path\":\"main.go\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 120, "completion_tokens": 34, "total_tokens": 154}
	}`)

	mock := NewMockServer()
	defer mock.Close()
	mock.SetDefaultResponse(&MockResponse{StatusCode: http.StatusOK, Body: raw})

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOpenAICompatibleProvider(mock.URL(), "", []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, ChatRequest{Model: "qwen2.5-coder:7b"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.ID != "chatcmpl-wire" {
		t.Errorf("ID: got %q, want %q", resp.ID, "chatcmpl-wire")
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices: got %d, want 1", len(resp.Choices))
	}
	c := resp.Choices[0]
	if c.FinishReason != "tool_calls" {
		t.Errorf("finish_reason: got %q, want %q", c.FinishReason, "tool_calls")
	}
	if len(c.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls decoded: got %d, want 1", len(c.Message.ToolCalls))
	}
	tc := c.Message.ToolCalls[0]
	if tc.ID != "call_abc123" || tc.Type != "function" || tc.Function.Name != "fs_read" {
		t.Errorf("tool call decode mismatch: %+v", tc)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v", err)
	}
	if args["path"] != "main.go" {
		t.Errorf("arguments.path: got %v, want main.go", args["path"])
	}
	if resp.Usage == nil {
		t.Fatal("usage not decoded")
	}
	if resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 34 || resp.Usage.TotalTokens != 154 {
		t.Errorf("usage decode mismatch: got %+v", resp.Usage)
	}
}

// TestOpenAICompatibleProvider_Chat_OmitsNameOnToolMessage is a regression
// lock for a bug found running a real manifest task against OpenCode Zen's
// "Console Go": messagesToAPI sent "name" on every message that had one set
// (Message.Name is only ever populated on a "tool" role message in
// practice — the tool's name, kept for storage/audit), which is a leftover
// of the pre-tool_calls "function" message shape. Console Go rejected it
// outright with HTTP 400 "messages[2]: \"name\" is not supported by this
// endpoint"; other backends likely just ignored the extra field.
func TestOpenAICompatibleProvider_Chat_OmitsNameOnToolMessage(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()
	mock.SetDefaultResponse(
		(&ChatResponseBuilder{ID: "x", Model: "m", Content: "ok", FinishReason: "stop"}).Build(),
	)

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewOpenAICompatibleProvider(mock.URL(), "", []string{hostFromURL(mock.URL())}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	_, err = provider.Chat(ctx, ChatRequest{
		Model: "m",
		Messages: []Message{
			{Role: "user", Content: "read main.go"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "fs_read", Arguments: "{}"}}}},
			{Role: "tool", ToolCallID: "call_1", Name: "fs_read", Content: "package main"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	last := mock.LastRequest()
	if last == nil {
		t.Fatal("no request captured")
	}
	var sent struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(last.Body, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if len(sent.Messages) != 3 {
		t.Fatalf("sent %d messages, want 3", len(sent.Messages))
	}
	toolMsg := sent.Messages[2]
	if toolMsg["role"] != "tool" {
		t.Fatalf("messages[2].role = %v, want tool", toolMsg["role"])
	}
	if _, present := toolMsg["name"]; present {
		t.Errorf(`messages[2] carries "name" = %v — Console Go rejects this field on a tool message`, toolMsg["name"])
	}
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("messages[2].tool_call_id = %v, want call_1", toolMsg["tool_call_id"])
	}
}

// TestValidateAllowlist_PortSemantics locks the allowlist matching rules:
// portless entries match the hostname on any port; entries WITH a port
// require exact host:port; empty list denies everything (RNF-4.9).
func TestValidateAllowlist_PortSemantics(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		allowed  []string
		wantDeny bool
	}{
		{"portless entry matches default port", "http://127.0.0.1:11434/v1", []string{"127.0.0.1"}, false},
		{"portless entry matches explicit other port", "http://localhost:8080/v1", []string{"localhost"}, false},
		{"exact host:port match", "http://127.0.0.1:11434/v1", []string{"127.0.0.1:11434"}, false},
		{"port entry mismatch denies", "http://127.0.0.1:9999/v1", []string{"127.0.0.1:11434"}, true},
		{"unlisted host denies", "http://example.com:11434/v1", []string{"127.0.0.1"}, true},
		{"empty allowlist denies all", "http://127.0.0.1:11434/v1", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAllowlist(tc.baseURL, tc.allowed)
			if tc.wantDeny && err == nil {
				t.Errorf("expected denial for %q with allowlist %v", tc.baseURL, tc.allowed)
			}
			if !tc.wantDeny && err != nil {
				t.Errorf("expected allow for %q with allowlist %v: %v", tc.baseURL, tc.allowed, err)
			}
		})
	}
}
