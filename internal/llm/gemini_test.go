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

func TestGeminiProvider_Chat_WireFormat(t *testing.T) {
	var capturedBody map[string]any
	var capturedHeaders http.Header
	var capturedPath string

	rawResp := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "Hello from Gemini"},
					{"functionCall": {"name": "fs_read", "args": {"path": "main.go"}}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
	}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawResp)
	}))
	defer srv.Close()

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	provider, err := NewGeminiProvider(srv.URL, "test-key", []string{hostFromURL(srv.URL)}, logger)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	defer provider.Close()

	temp := 0.5
	maxTokens := 200
	req := ChatRequest{
		Model: "gemini-1.5-pro",
		Messages: []Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "system", Content: "More system"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "I will call", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "fs_read", Arguments: `{"path":"main.go"}`}}}},
			{Role: "tool", Content: "file content", ToolCallID: "call_1", Name: "fs_read"},
		},
		Tools: []ToolDef{{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "fs_read",
				Description: "Read file",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			},
		}},
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	resp, err := provider.Chat(ctx, req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Path assertion: must be /v1beta/models/{model}:generateContent
	if !strings.Contains(capturedPath, "/v1beta/models/gemini-1.5-pro:generateContent") {
		t.Errorf("path: got %q", capturedPath)
	}
	if capturedHeaders.Get("x-goog-api-key") != "test-key" {
		t.Errorf("x-goog-api-key header: got %q", capturedHeaders.Get("x-goog-api-key"))
	}
	// Ensure key is not in URL query
	if strings.Contains(capturedPath, "key=") {
		t.Errorf("key should not be in URL query, got %q", capturedPath)
	}
	// SystemInstruction extraction.
	sysInstr, ok := capturedBody["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("systemInstruction missing: %v", capturedBody["systemInstruction"])
	}
	parts, ok := sysInstr["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("systemInstruction parts: %v", sysInstr["parts"])
	}
	text := parts[0].(map[string]any)["text"].(string)
	if text != "You are helpful\n\nMore system" {
		t.Errorf("systemInstruction text: got %q", text)
	}
	// Contents: system omitted, should have 3 contents (user, model with functionCall, user with functionResponse)
	contents, ok := capturedBody["contents"].([]any)
	if !ok || len(contents) != 3 {
		t.Fatalf("contents: got %d, want 3, %v", len(contents), capturedBody["contents"])
	}
	// Check model functionCall
	modelContent := contents[1].(map[string]any)
	if modelContent["role"] != "model" {
		t.Errorf("model role: %v", modelContent["role"])
	}
	modelParts := modelContent["parts"].([]any)
	foundFC := false
	for _, p := range modelParts {
		m := p.(map[string]any)
		if fc, ok := m["functionCall"]; ok {
			fcMap := fc.(map[string]any)
			if fcMap["name"] == "fs_read" {
				foundFC = true
				args := fcMap["args"].(map[string]any)
				if args["path"] != "main.go" {
					t.Errorf("functionCall args: %v", args)
				}
			}
		}
	}
	if !foundFC {
		t.Errorf("functionCall not found")
	}
	// Check tool -> functionResponse
	toolContent := contents[2].(map[string]any)
	toolParts := toolContent["parts"].([]any)[0].(map[string]any)
	fr, ok := toolParts["functionResponse"]
	if !ok {
		t.Fatalf("functionResponse missing")
	}
	frMap := fr.(map[string]any)
	if frMap["name"] != "fs_read" {
		t.Errorf("functionResponse name: %v", frMap["name"])
	}
	respMap := frMap["response"].(map[string]any)
	if respMap["result"] != "file content" {
		t.Errorf("functionResponse result: %v", respMap["result"])
	}
	// Tools mapping: functionDeclarations
	toolsArr, ok := capturedBody["tools"].([]any)
	if !ok || len(toolsArr) != 1 {
		t.Fatalf("tools: %v", capturedBody["tools"])
	}
	decls := toolsArr[0].(map[string]any)["functionDeclarations"].([]any)
	if len(decls) != 1 {
		t.Fatalf("functionDeclarations: %v", decls)
	}
	declMap := decls[0].(map[string]any)
	if declMap["name"] != "fs_read" {
		t.Errorf("decl name: %v", declMap["name"])
	}

	// Response mapping.
	if len(resp.Choices) != 1 {
		t.Fatalf("choices: %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello from Gemini" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("toolCalls: %d", len(resp.Choices[0].Message.ToolCalls))
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "fs_read" {
		t.Errorf("tool name: %v", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finishReason: got %q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage: %+v", resp.Usage)
	}
	// generationConfig
	genCfg, ok := capturedBody["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing")
	}
	if genCfg["temperature"] != 0.5 {
		t.Errorf("temperature: %v", genCfg["temperature"])
	}
	if genCfg["maxOutputTokens"] != float64(200) {
		t.Errorf("maxOutputTokens: %v", genCfg["maxOutputTokens"])
	}
}

func TestGeminiProvider_ChatStream_NotSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	_, err := p.ChatStream(nil, ChatRequest{Model: "gemini-1.5-pro"})
	if !errors.Is(err, ErrStreamingNotSupported) {
		t.Fatalf("expected ErrStreamingNotSupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error should mention gemini, got %q", err.Error())
	}
}

func TestGeminiProvider_ListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			if r.Header.Get("x-goog-api-key") == "" {
				t.Error("x-goog-api-key missing on ListModels")
			}
			if strings.Contains(r.URL.RawQuery, "key=") {
				t.Error("key should not be in query param")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-1.5-pro"},{"name":"models/gemini-1.5-flash"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, err := NewGeminiProvider(srv.URL, "k", []string{hostFromURL(srv.URL)}, logger)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer p.Close()
	models, err := p.ListModels()
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "gemini-1.5-pro" || models[1] != "gemini-1.5-flash" {
		t.Errorf("models: %v", models)
	}
}

func TestGeminiProvider_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	resp, err := p.Chat(ctx, ChatRequest{Model: "gemini-1.5-pro", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finishReason for empty: %q", resp.Choices[0].FinishReason)
	}
}

func TestGeminiProvider_ErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	p, _ := NewGeminiProvider(srv.URL, "", []string{hostFromURL(srv.URL)}, logger)
	defer p.Close()
	ctx, cancel := ContextWithTimeout(5 * 1000000000)
	defer cancel()
	_, err := p.Chat(ctx, ChatRequest{Model: "gemini-1.5-pro", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected bad request error, got %v", err)
	}
}
