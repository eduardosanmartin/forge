package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

func geminiSSEFixture() string {
	var b strings.Builder
	write := func(data string) {
		b.WriteString("data: " + data + "\n\n")
	}
	write(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello "}]}}]}`)
	write(`{"candidates":[{"content":{"role":"model","parts":[{"text":"world"}]}}]}`)
	write(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"fs_read","args":{"path":"main.go"}}}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`)
	return b.String()
}

func geminiModelsHandler(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/v1beta/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
		return true
	}
	return false
}

func TestGeminiChatStream_FullSequence(t *testing.T) {
	sseBody := geminiSSEFixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("path %q should contain streamGenerateContent", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "alt=sse") {
			t.Errorf("query %q should contain alt=sse", r.URL.RawQuery)
		}
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("x-goog-api-key %q", r.Header.Get("x-goog-api-key"))
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if _, ok := m["contents"]; !ok {
			t.Errorf("contents missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "k", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := p.ChatStream(ctx, ChatRequest{Model: "gemini-1.5-pro", Messages: []Message{{Role: "user", Content: "hi"}}})
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
			t.Fatalf("error chunk: %s", chunk.Error)
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
		t.Errorf("content %q want Hello world", content)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("toolCalls %d want 1", len(toolCalls))
	}
	if toolCalls[0].Function.Name != "fs_read" {
		t.Errorf("tool name %q", toolCalls[0].Function.Name)
	}
	var args map[string]string
	_ = json.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args)
	if args["path"] != "main.go" {
		t.Errorf("args %v", args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish %q want tool_calls", finish)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Errorf("usage %+v", usage)
	}
}

func TestGeminiChatStream_Malformed(t *testing.T) {
	sseBody := "data: {truncated json\n\n" + "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
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
		t.Errorf("expected ok, got %q", text)
	}
}

func TestGeminiChatStream_UnknownFieldIgnored(t *testing.T) {
	sseBody := "data: {\"unknown\":\"field\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
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
		t.Errorf("unknown field should be ignored, got %q", text)
	}
}

func TestGeminiChatStream_MissingData(t *testing.T) {
	sseBody := "event: ignored\ndata: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
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
		t.Errorf("got %q want hi", text)
	}
}

func TestGeminiChatStream_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"a\"}]}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("wait chunk")
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	for range ch {
	}
}

func TestGeminiChatStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestGeminiChatStream_UsageAbsent(t *testing.T) {
	sseBody := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var usage *Usage
	for chunk := range ch {
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	_ = usage
}

func TestGeminiChatStream_ProviderError(t *testing.T) {
	sseBody := "data: {\"error\":{\"message\":\"quota exceeded\",\"code\":429}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geminiModelsHandler(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, _ := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	var errMsg string
	for chunk := range ch {
		if chunk.Error != "" {
			errMsg = chunk.Error
		}
	}
	if errMsg != "quota exceeded" {
		t.Fatalf("got %q want quota exceeded", errMsg)
	}
}
