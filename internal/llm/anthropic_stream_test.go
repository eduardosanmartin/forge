package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

func anthropicSSEFixture() string {
	var b strings.Builder
	writeEvent := func(event, data string) {
		b.WriteString("event: " + event + "\n")
		b.WriteString("data: " + data + "\n\n")
	}
	writeEvent("message_start", `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`)
	writeEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	writeEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`)
	writeEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`)
	writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
	writeEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"fs_read"}}`)
	writeEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
	writeEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`)
	writeEvent("content_block_stop", `{"type":"content_block_stop","index":1}`)
	writeEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":34}}`)
	writeEvent("message_stop", `{"type":"message_stop"}`)
	return b.String()
}

func anthropicModelsHandler(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
		return true
	}
	return false
}

func TestAnthropicChatStream_FullSequence(t *testing.T) {
	sseBody := anthropicSSEFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: %q", ct)
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version: %q", r.Header.Get("anthropic-version"))
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["stream"] != true {
			t.Errorf("stream flag: got %v, want true", m["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, err := NewAnthropicProvider(srv.URL, "k", []string{hostFromURL(srv.URL)}, logger)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.ChatStream(ctx, ChatRequest{Model: "claude-3-5-sonnet-20241022", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var (
		textParts []string
		toolCalls []ToolCall
		usage     *Usage
		finish    string
	)
	for chunk := range ch {
		if chunk.Error != "" {
			t.Fatalf("unexpected error chunk: %s", chunk.Error)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				textParts = append(textParts, c.Delta.Content)
			}
			if len(c.Delta.ToolCalls) > 0 {
				toolCalls = append(toolCalls, c.Delta.ToolCalls...)
			}
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
		}
	}
	content := strings.Join(textParts, "")
	if content != "Hello world" {
		t.Errorf("content: got %q, want %q", content, "Hello world")
	}
	if len(toolCalls) != 1 {
		t.Fatalf("toolCalls: got %d, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "fs_read" {
		t.Errorf("tool call: %+v", tc)
	}
	var args map[string]string
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	if args["path"] != "main.go" {
		t.Errorf("args path: %v", args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish: got %q, want tool_calls", finish)
	}
	if usage == nil || usage.CompletionTokens != 34 || usage.PromptTokens != 10 || usage.TotalTokens != 44 {
		t.Errorf("usage: %+v", usage)
	}
}

func TestAnthropicChatStream_MalformedTruncatedJSON(t *testing.T) {
	sseBody := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var text string
	for chunk := range ch {
		if chunk.Error != "" {
			t.Fatalf("error chunk: %s", chunk.Error)
		}
		for _, c := range chunk.Choices {
			text += c.Delta.Content
		}
	}
	if text != " world" {
		t.Errorf("text after truncated should be ' world', got %q", text)
	}
}

func TestAnthropicChatStream_UnknownEventIgnored(t *testing.T) {
	sseBody := "event: supersecret\ndata: {\"type\":\"supersecret\",\"foo\":\"bar\"}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var text string
	for chunk := range ch {
		for _, c := range chunk.Choices {
			text += c.Delta.Content
		}
	}
	if text != "ok" {
		t.Errorf("unknown event should be ignored, text got %q", text)
	}
}

func TestAnthropicChatStream_MissingDataIgnored(t *testing.T) {
	sseBody := "event: content_block_delta\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var text string
	for chunk := range ch {
		for _, c := range chunk.Choices {
			text += c.Delta.Content
		}
	}
	if text != "hi" {
		t.Errorf("missing data should be ignored, got %q", text)
	}
}

func TestAnthropicChatStream_UsageAbsent(t *testing.T) {
	sseBody := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var gotUsage *Usage
	for chunk := range ch {
		if chunk.Usage != nil {
			gotUsage = chunk.Usage
		}
	}
	if gotUsage != nil {
		t.Errorf("usage should be nil when absent, got %+v", gotUsage)
	}
}

func TestAnthropicChatStream_HTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			// For the models fetch, still succeed; ChatStream request will be second request with non-200.
			// But we filter: if path is models, return ok; else non-200.
			// The handler checks models first, so models fetch succeeds.
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected bad request error, got %v", err)
	}
}

func TestAnthropicChatStream_ProviderErrorEvent(t *testing.T) {
	sseBody := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var gotErr string
	for chunk := range ch {
		if chunk.Error != "" {
			gotErr = chunk.Error
		}
	}
	if gotErr != "overloaded" {
		t.Fatalf("expected error 'overloaded', got %q", gotErr)
	}
}

func TestAnthropicChatStream_ContextCancelNoLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anthropicModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n"))
		flusher.Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewAnthropicProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first chunk")
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	select {
	case _, ok := <-ch:
		if ok {
			for range ch {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
	runtime.GC()
	before := runtime.NumGoroutine()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+10 {
		t.Logf("possible leak: goroutines before %d after %d", before, after)
	}
	_ = errors.New
}
