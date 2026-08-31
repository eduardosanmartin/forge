package llm

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

// TestOllamaProvider_Chat_RealWireFormat is a regression lock: the mock
// server normally round-trips Go structs (Go-dialect JSON), which masked a
// bug where response structs lacked snake_case json tags — tool_calls,
// finish_reason, and usage silently decoded to zero values against real
// servers. This test feeds RAW OpenAI-compatible wire bytes and asserts
// every critical field decodes.
func TestOllamaProvider_Chat_RealWireFormat(t *testing.T) {
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
	provider, err := NewOllamaProvider(mock.URL(), "", []string{hostFromURL(mock.URL())}, logger)
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
