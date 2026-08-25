// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// MockServer is a test helper that simulates an OpenAI-compatible API server.
type MockServer struct {
	server      *httptest.Server
	mu          sync.Mutex
	requests    []MockRequest
	handlers    map[string]MockHandler
	defaultResp *MockResponse
	closed      bool
}

// MockRequest captures a request made to the mock server.
type MockRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// MockResponse defines a canned response for a path.
type MockResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	Stream     bool          // if true, body is treated as SSE stream lines
	Delay      time.Duration // optional delay before responding
}

// MockHandler is a function that generates responses dynamically.
type MockHandler func(req *http.Request) *MockResponse

// NewMockServer creates a new mock server.
func NewMockServer() *MockServer {
	m := &MockServer{
		handlers: make(map[string]MockHandler),
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

// URL returns the base URL of the mock server.
func (m *MockServer) URL() string {
	return m.server.URL
}

// Close shuts down the mock server.
func (m *MockServer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.server.Close()
		m.closed = true
	}
}

// Requests returns all captured requests.
func (m *MockServer) Requests() []MockRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	reqs := make([]MockRequest, len(m.requests))
	copy(reqs, m.requests)
	return reqs
}

// LastRequest returns the most recent request, or nil if none.
func (m *MockServer) LastRequest() *MockRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	req := m.requests[len(m.requests)-1]
	return &req
}

// SetHandler sets a dynamic handler for a path.
func (m *MockServer) SetHandler(path string, handler MockHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[path] = handler
}

// SetDefaultResponse sets a default response for unhandled paths.
func (m *MockServer) SetDefaultResponse(resp *MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultResp = resp
}

// handle is the main HTTP handler.
func (m *MockServer) handle(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	m.mu.Lock()
	m.requests = append(m.requests, MockRequest{
		Method:  req.Method,
		Path:    req.URL.Path,
		Headers: req.Header.Clone(),
		Body:    body,
	})
	m.mu.Unlock()

	// Check for dynamic handler
	m.mu.Lock()
	handler, hasHandler := m.handlers[req.URL.Path]
	defaultResp := m.defaultResp
	m.mu.Unlock()

	if hasHandler {
		resp := handler(req)
		m.writeResponse(w, resp)
		return
	}

	if defaultResp != nil {
		m.writeResponse(w, defaultResp)
		return
	}

	// Default 404
	http.NotFound(w, req)
}

func (m *MockServer) writeResponse(w http.ResponseWriter, resp *MockResponse) {
	if resp.Delay > 0 {
		time.Sleep(resp.Delay)
	}

	for k, v := range resp.Headers {
		w.Header()[k] = v
	}

	if resp.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Write each line as SSE data
		lines := strings.Split(string(resp.Body), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond) // small delay between chunks
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.Body)
}

// --- Test response builders ---

// ChatResponseBuilder builds a non-streaming chat completion response.
type ChatResponseBuilder struct {
	ID           string
	Model        string
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

func (b *ChatResponseBuilder) Build() *MockResponse {
	resp := ChatResponse{
		ID:    b.ID,
		Model: b.Model,
		Choices: []Choice{{
			Index: 0,
			Message: Message{
				Role:      "assistant",
				Content:   b.Content,
				ToolCalls: b.ToolCalls,
			},
			FinishReason: b.FinishReason,
		}},
		Usage: b.Usage,
	}
	body, _ := json.Marshal(resp)
	return &MockResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}

// StreamChunkBuilder builds streaming response chunks.
type StreamChunkBuilder struct {
	ID     string
	Model  string
	Deltas []StreamDelta
}

type StreamDelta struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason *string
}

func (b *StreamChunkBuilder) Build() *MockResponse {
	var lines []string
	for i, delta := range b.Deltas {
		chunk := StreamChunk{
			ID:    b.ID,
			Model: b.Model,
			Choices: []StreamChoice{{
				Index: i,
				Delta: Message{
					Role:      "assistant",
					Content:   delta.Content,
					ToolCalls: delta.ToolCalls,
				},
				FinishReason: delta.FinishReason,
			}},
		}
		data, _ := json.Marshal(chunk)
		lines = append(lines, string(data))
	}
	return &MockResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       []byte(strings.Join(lines, "\n")),
		Stream:     true,
	}
}

// ModelsResponseBuilder builds a /models or /v1/models response.
type ModelsResponseBuilder struct {
	Models []string // Ollama native format
	Data   []string // OpenAI compat format
}

func (b *ModelsResponseBuilder) Build() *MockResponse {
	var resp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	for _, m := range b.Models {
		resp.Models = append(resp.Models, struct {
			Name string `json:"name"`
		}{Name: m})
	}
	for _, d := range b.Data {
		resp.Data = append(resp.Data, struct {
			ID string `json:"id"`
		}{ID: d})
	}
	body, _ := json.Marshal(resp)
	return &MockResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}

// ErrorResponseBuilder builds an error response.
type ErrorResponseBuilder struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
}

func (b *ErrorResponseBuilder) Build() *MockResponse {
	errResp := map[string]any{
		"error": map[string]any{
			"message": b.Message,
			"type":    b.Type,
			"code":    b.Code,
		},
	}
	body, _ := json.Marshal(errResp)
	return &MockResponse{
		StatusCode: b.StatusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}

// --- Context helpers for tests ---

// ContextWithTimeout returns a context with timeout for tests.
func ContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// AssertRequestBodyEqual compares request body JSON with expected.
func AssertRequestBodyEqual(t interface{ Fatalf(string, ...any) }, body []byte, expected any) {
	var actual, exp map[string]any
	if err := json.Unmarshal(body, &actual); err != nil {
		t.Fatalf("failed to unmarshal actual body: %v", err)
	}
	expBytes, _ := json.Marshal(expected)
	if err := json.Unmarshal(expBytes, &exp); err != nil {
		t.Fatalf("failed to unmarshal expected: %v", err)
	}
	if !jsonEqual(actual, exp) {
		t.Fatalf("request body mismatch\ngot: %s\nexpected: %s", string(body), string(expBytes))
	}
}

func jsonEqual(a, b map[string]any) bool {
	aBytes, _ := json.Marshal(a)
	bBytes, _ := json.Marshal(b)
	return string(aBytes) == string(bBytes)
}
