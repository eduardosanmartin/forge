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

// ErrStreamingNotSupported is returned by ChatStream when the provider does not
// implement streaming in this WU6 scope. The agent loop uses Chat (non-streaming)
// exclusively (see internal/agent/loop.go), so streaming is secondary and
// intentionally not implemented for Anthropic/Gemini in WU6.
var ErrStreamingNotSupported = errors.New("streaming not supported: use Chat")

const (
	defaultAnthropicBaseURL       = "https://api.anthropic.com"
	anthropicVersion              = "2023-06-01"
	anthropicDefaultMaxTokens     = 4096
	defaultGeminiBaseURL          = "https://generativelanguage.googleapis.com"
	geminiDefaultMaxTokensUnused  = 0 // Gemini max tokens is not required; placeholder for parity.
)

// AnthropicProvider implements Provider for the Anthropic Messages API.
// Default baseURL is https://api.anthropic.com when config base_url is empty.
type AnthropicProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	models     []string
	modelsMu   sync.RWMutex
	closed     bool
	closedMu   sync.Mutex
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(baseURL, apiKey string, allowedHosts []string, logger *slog.Logger) (*AnthropicProvider, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultAnthropicBaseURL
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
	p := &AnthropicProvider{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: client,
		logger:     logger,
	}
	if err := p.refreshModels(); err != nil {
		logger.Warn("failed to fetch anthropic models at startup", "error", err)
	}
	return p, nil
}

func (p *AnthropicProvider) refreshModels() error {
	models, err := p.fetchModels()
	if err != nil {
		p.logger.Debug("anthropic fetch models failed", "error", err)
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

func (p *AnthropicProvider) fetchModels() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endpoint := p.baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("x-api-key", p.apiKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
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
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var models []string
	for _, d := range result.Data {
		if d.ID != "" {
			models = append(models, d.ID)
		}
	}
	return models, nil
}

// buildAnthropicBody maps ChatRequest to Anthropic's Messages API body.
func (p *AnthropicProvider) buildAnthropicBody(req ChatRequest) map[string]any {
	maxTokens := anthropicDefaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
	}
	// System: concatenate all role="system" messages.
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
		}
	}
	if len(systemParts) > 0 {
		body["system"] = strings.Join(systemParts, "\n\n")
	}
	// Messages: skip system, map user/assistant/tool.
	var msgs []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			continue
		case "user":
			msgs = append(msgs, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": m.Content},
				},
			})
		case "assistant":
			var blocks []map[string]any
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input any
				if tc.Function.Arguments != "" {
					var obj map[string]any
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &obj); err == nil {
						input = obj
					} else {
						input = map[string]any{}
					}
				} else {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": ""})
			}
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})
		case "tool":
			// Map role="tool" to user with tool_result block.
			toolUseID := m.ToolCallID
			if toolUseID == "" {
				toolUseID = m.Name
			}
			msgs = append(msgs, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": toolUseID, "content": m.Content},
				},
			})
		default:
			msgs = append(msgs, map[string]any{
				"role": m.Role,
				"content": []map[string]any{
					{"type": "text", "text": m.Content},
				},
			})
		}
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Function.Name,
				"description":  t.Function.Description,
				"input_schema": t.Function.Parameters,
			})
		}
		body["tools"] = tools
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	// stream flag is not used for non-streaming; Anthropic streaming uses stream:true but we always send false here.
	// Explicitly omit stream for Chat to keep wire minimal.
	return body
}

// Chat implements Provider.Chat for Anthropic.
func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	p.closedMu.Lock()
	if p.closed {
		p.closedMu.Unlock()
		return ChatResponse{}, errors.New("provider closed")
	}
	p.closedMu.Unlock()

	body, err := json.Marshal(p.buildAnthropicBody(req))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	endpoint := p.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}
	p.logger.Debug("anthropic chat request", "endpoint", endpoint, "body", logging.Redact(string(body)))
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, p.mapError(err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	p.logger.Debug("anthropic chat response", "status", resp.StatusCode, "body", logging.Redact(string(respBody)))
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, p.mapHTTPError(resp.StatusCode, respBody)
	}
	// Decode Anthropic response.
	var ar anthropicResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return ChatResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return p.anthropicToChatResponse(ar), nil
}

type anthropicResponse struct {
	ID         string                   `json:"id"`
	Type       string                   `json:"type"`
	Role       string                   `json:"role"`
	Model      string                   `json:"model"`
	Content    []anthropicContentBlock  `json:"content"`
	StopReason *string                  `json:"stop_reason"`
	Usage      anthropicUsage           `json:"usage"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) anthropicToChatResponse(ar anthropicResponse) ChatResponse {
	var textParts []string
	var toolCalls []ToolCall
	for _, b := range ar.Content {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "tool_use":
			argsStr := "{}"
			if len(b.Input) > 0 && string(b.Input) != "null" {
				// Ensure valid JSON object.
				var raw any
				if json.Unmarshal(b.Input, &raw) == nil {
					marshaled, _ := json.Marshal(raw)
					argsStr = string(marshaled)
				} else {
					argsStr = string(b.Input)
				}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      b.Name,
					Arguments: argsStr,
				},
			})
		}
	}
	content := strings.Join(textParts, "")
	finishReason := ""
	if ar.StopReason != nil {
		switch *ar.StopReason {
		case "tool_use":
			finishReason = "tool_calls"
		case "end_turn":
			finishReason = "stop"
		case "max_tokens":
			finishReason = "length"
		default:
			finishReason = *ar.StopReason
		}
	}
	usage := &Usage{
		PromptTokens:     ar.Usage.InputTokens,
		CompletionTokens: ar.Usage.OutputTokens,
		TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
	}
	return ChatResponse{
		ID:    ar.ID,
		Model: ar.Model,
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

// ChatStream implements Provider.ChatStream. WU6 does not implement streaming for Anthropic
// because the agent loop uses Chat only (see loop.go). Returns ErrStreamingNotSupported.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	return nil, fmt.Errorf("%w: anthropic streaming requires SSE event parsing (message_start/content_block_delta) not in WU6 scope", ErrStreamingNotSupported)
}

// ListModels implements Provider.ListModels for Anthropic.
func (p *AnthropicProvider) ListModels() ([]string, error) {
	p.modelsMu.RLock()
	defer p.modelsMu.RUnlock()
	models := make([]string, len(p.models))
	copy(models, p.models)
	return models, nil
}

// Close implements Provider.Close.
func (p *AnthropicProvider) Close() error {
	p.closedMu.Lock()
	defer p.closedMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.httpClient.CloseIdleConnections()
	return nil
}

func (p *AnthropicProvider) mapError(err error) error {
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

func (p *AnthropicProvider) mapHTTPError(statusCode int, body []byte) error {
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
