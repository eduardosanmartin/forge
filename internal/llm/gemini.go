// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/logging"
)

// GeminiProvider implements Provider for Google Gemini generateContent API.
// Default baseURL is https://generativelanguage.googleapis.com when config base_url is empty.
// Auth uses header x-goog-api-key (NOT query param) to keep the key out of URLs.
type GeminiProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	models     []string
	modelsMu   sync.RWMutex
	closed     bool
	closedMu   sync.Mutex
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(baseURL, apiKey string, allowedHosts []string, logger *slog.Logger) (*GeminiProvider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("GOOGLE_API_KEY")
		}
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGeminiBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := validateAllowlist(baseURL, allowedHosts); err != nil {
		return nil, err
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	client := &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	p := &GeminiProvider{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: client,
		logger:     logger,
	}
	if err := p.refreshModels(); err != nil {
		logger.Warn("failed to fetch gemini models at startup", "error", err)
	}
	return p, nil
}

func (p *GeminiProvider) refreshModels() error {
	models, err := p.fetchModels()
	if err != nil {
		p.logger.Debug("gemini fetch models failed", "error", err)
		return err
	}
	seen := make(map[string]bool)
	unique := make([]string, 0, len(models))
	for _, m := range models {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
	}
	p.modelsMu.Lock()
	p.models = unique
	p.modelsMu.Unlock()
	return nil
}

func (p *GeminiProvider) fetchModels() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint := p.baseURL + "/v1beta/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("x-goog-api-key", p.apiKey)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models fetch failed: %d %s", resp.StatusCode, logging.Redact(string(body)))
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var models []string
	for _, m := range result.Models {
		name := m.Name
		// Strip "models/" prefix per spec.
		if strings.HasPrefix(name, "models/") {
			name = strings.TrimPrefix(name, "models/")
		}
		if name != "" {
			models = append(models, name)
		}
	}
	return models, nil
}

// buildGeminiBody maps ChatRequest to Gemini generateContent body.
func (p *GeminiProvider) buildGeminiBody(req ChatRequest) map[string]any {
	body := map[string]any{}

	// System instruction from system messages.
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" && m.Content != "" {
			systemParts = append(systemParts, m.Content)
		}
	}
	if len(systemParts) > 0 {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": strings.Join(systemParts, "\n\n")}},
		}
	}

	// Contents: map each non-system message.
	var contents []map[string]any
	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		switch m.Role {
		case "user":
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": m.Content}},
			})
		case "assistant":
			var parts []map[string]any
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				if args == nil {
					args = map[string]any{}
				}
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": tc.Function.Name,
						"args": args,
					},
				})
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}
			contents = append(contents, map[string]any{
				"role":  "model",
				"parts": parts,
			})
		case "tool":
			// Map to user role with functionResponse.
			name := m.Name
			if name == "" {
				name = m.ToolCallID
			}
			if name == "" {
				name = "unknown"
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{"functionResponse": map[string]any{
						"name": name,
						"response": map[string]any{"result": m.Content},
					}},
				},
			})
		default:
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": m.Content}},
			})
		}
	}
	body["contents"] = contents

	if len(req.Tools) > 0 {
		var decls []map[string]any
		for _, t := range req.Tools {
			decls = append(decls, map[string]any{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			})
		}
		body["tools"] = []map[string]any{
			{"functionDeclarations": decls},
		}
	}
	if req.Temperature != nil {
		body["generationConfig"] = map[string]any{
			"temperature": *req.Temperature,
		}
		if req.MaxTokens != nil {
			// Gemini uses maxOutputTokens inside generationConfig.
			cfg := body["generationConfig"].(map[string]any)
			cfg["maxOutputTokens"] = *req.MaxTokens
		}
	} else if req.MaxTokens != nil {
		body["generationConfig"] = map[string]any{
			"maxOutputTokens": *req.MaxTokens,
		}
	}
	return body
}

// Chat implements Provider.Chat for Gemini.
func (p *GeminiProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	p.closedMu.Lock()
	if p.closed {
		p.closedMu.Unlock()
		return ChatResponse{}, errors.New("provider closed")
	}
	p.closedMu.Unlock()

	bodyMap := p.buildGeminiBody(req)
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.baseURL, url.PathEscape(req.Model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", p.apiKey)
	}
	p.logger.Debug("gemini chat request", "endpoint", endpoint, "body", logging.Redact(string(body)))
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, p.mapError(err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	p.logger.Debug("gemini chat response", "status", resp.StatusCode, "body", logging.Redact(string(respBody)))
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, p.mapHTTPError(resp.StatusCode, respBody)
	}
	var gr geminiResponse
	if err := json.Unmarshal(respBody, &gr); err != nil {
		return ChatResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return p.geminiToChatResponse(gr, req.Model), nil
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string `json:"text,omitempty"`
				FunctionCall *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall,omitempty"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *GeminiProvider) geminiToChatResponse(gr geminiResponse, model string) ChatResponse {
	if len(gr.Candidates) == 0 {
		return ChatResponse{
			ID:    "gemini-empty",
			Model: model,
			Choices: []Choice{{
				Index:        0,
				Message:      Message{Role: "assistant", Content: ""},
				FinishReason: "stop",
			}},
		}
	}
	cand := gr.Candidates[0]
	var textParts []string
	var toolCalls []ToolCall
	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall != nil {
			argsStr := "{}"
			if part.FunctionCall.Args != nil {
				b, _ := json.Marshal(part.FunctionCall.Args)
				argsStr = string(b)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   part.FunctionCall.Name + "_call",
				Type: "function",
				Function: ToolCallFunction{
					Name:      part.FunctionCall.Name,
					Arguments: argsStr,
				},
			})
		}
	}
	content := strings.Join(textParts, "")
	// Map finishReason: STOP, MAX_TOKENS, SAFETY, RECITATION, OTHER etc.
	finishReason := "stop"
	switch cand.FinishReason {
	case "STOP":
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	case "MAX_TOKENS":
		finishReason = "length"
	case "SAFETY", "RECITATION":
		finishReason = "content_filter"
	default:
		if cand.FinishReason != "" {
			finishReason = strings.ToLower(cand.FinishReason)
		}
	}
	var usage *Usage
	if gr.UsageMetadata != nil {
		usage = &Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		}
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}
	return ChatResponse{
		ID:    "gemini-" + model,
		Model: model,
		Choices: []Choice{{
			Index: 0,
			Message: Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
}

// ChatStream implements Provider.ChatStream. WU6 does not implement streaming for Gemini
// because the agent loop uses Chat only. Returns ErrStreamingNotSupported.
func (p *GeminiProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	return nil, fmt.Errorf("%w: gemini streaming requires alt=sse handling not in WU6 scope", ErrStreamingNotSupported)
}

// ListModels implements Provider.ListModels for Gemini.
func (p *GeminiProvider) ListModels() ([]string, error) {
	p.modelsMu.RLock()
	defer p.modelsMu.RUnlock()
	models := make([]string, len(p.models))
	copy(models, p.models)
	return models, nil
}

// Close implements Provider.Close.
func (p *GeminiProvider) Close() error {
	p.closedMu.Lock()
	defer p.closedMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.httpClient.CloseIdleConnections()
	return nil
}

func (p *GeminiProvider) mapError(err error) error {
	var netErr *url.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return fmt.Errorf("request timeout: %w", err)
		}
		return fmt.Errorf("connection error: %w", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request deadline exceeded: %w", err)
	}
	return fmt.Errorf("request failed: %w", err)
}

func (p *GeminiProvider) mapHTTPError(statusCode int, body []byte) error {
	bodyStr := logging.Redact(string(body))
	switch statusCode {
	case http.StatusNotFound:
		return fmt.Errorf("model not found (404): %s", bodyStr)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited (429): %s", bodyStr)
	case http.StatusUnauthorized:
		return fmt.Errorf("unauthorized (401): %s", bodyStr)
	case http.StatusForbidden:
		return fmt.Errorf("forbidden (403): %s", bodyStr)
	case http.StatusBadRequest:
		return fmt.Errorf("bad request (400): %s", bodyStr)
	case http.StatusInternalServerError:
		return fmt.Errorf("server error (500): %s", bodyStr)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("upstream unavailable (%d): %s", statusCode, bodyStr)
	default:
		return fmt.Errorf("HTTP %d: %s", statusCode, bodyStr)
	}
}
