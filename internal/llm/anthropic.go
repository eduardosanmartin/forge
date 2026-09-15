// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter and a model registry supporting hot-swap.
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

// SetRequestTimeout overrides the per-request HTTP timeout (default 15
// minutes, set above at construction). Ignored when d <= 0.
func (p *AnthropicProvider) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		p.httpClient.Timeout = d
	}
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

// anthropicToolAccum holds incremental input assembly for a tool_use content block.
type anthropicToolAccum struct {
	ID   string
	Name string
	buf  strings.Builder
}

// ChatStream implements Provider.ChatStream for Anthropic.
//
// It POSTs /v1/messages with "stream": true and parses the SSE event sequence:
// message_start, content_block_start, content_block_delta, content_block_stop,
// message_delta, message_stop, plus ping/error. Text deltas are emitted as
// StreamChunk deltas immediately. Tool_use input is assembled from partial_json
// fragments and emitted once per block on content_block_stop. The final chunk
// carries finish_reason and usage. Unknown event types are ignored gracefully.
// Truncated JSON is skipped (debug logged). HTTP non-200 surfaces the error body.
// Provider mid-stream `error` events are emitted as StreamChunk{Error: "..."} then the
// channel is closed. Context cancellation closes the channel with no goroutine leak.
//
// Mid-stream failure (error event, JSON parse fatal, or transport error after
// streaming started) fails the turn predictably; the caller should NOT fallback
// to Chat within the same turn — the next turn may run non-streaming if
// streaming is disabled in config.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p.closedMu.Lock()
	if p.closed {
		p.closedMu.Unlock()
		return nil, errors.New("provider closed")
	}
	p.closedMu.Unlock()

	bodyMap := p.buildAnthropicBody(req)
	bodyMap["stream"] = true
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	endpoint := p.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}
	p.logger.Debug("anthropic chat stream request", "endpoint", endpoint, "body", logging.Redact(string(body)))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, p.mapError(err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, p.mapHTTPError(resp.StatusCode, respBody)
	}

	ch := make(chan StreamChunk, 16)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		var (
			messageID   string
			model       string
			toolAccums  = make(map[int]*anthropicToolAccum)
			usage       *Usage
			finishReason string
			// anthropicUsage stores input/output for eventual Usage.
			inputTokens int
		)

		// Helper to emit a chunk or abort on ctx cancellation (no leak).
		emit := func(chunk StreamChunk) bool {
			select {
			case ch <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		handler := func(ev SSEEvent) error {
			// Anthropic error events are delivered as SSE event "error" with JSON {"type":"error","error":{"type":...,"message":...}}
			if ev.Event == "error" {
				var errPayload struct {
					Type  string `json:"type"`
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &errPayload); err == nil {
					msg := errPayload.Error.Message
					if msg == "" {
						msg = "anthropic stream error"
					}
					_ = emit(StreamChunk{ID: messageID, Model: model, Error: msg})
				} else {
					// Fallback: surface raw data as error.
					_ = emit(StreamChunk{ID: messageID, Model: model, Error: ev.Data})
				}
				// Terminal: stop parsing.
				return io.EOF
			}

			switch ev.Event {
			case "message_start":
				var payload struct {
					Type    string `json:"type"`
					Message struct {
						ID    string         `json:"id"`
						Model string         `json:"model"`
						Usage anthropicUsage `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					p.logger.Debug("anthropic stream parse message_start failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				messageID = payload.Message.ID
				model = payload.Message.Model
				inputTokens = payload.Message.Usage.InputTokens
				// Map model back if empty use request model.
				if model == "" {
					model = req.Model
				}
			case "content_block_start":
				var payload struct {
					Type         string `json:"type"`
					Index        int    `json:"index"`
					ContentBlock struct {
						Type string `json:"type"`
						ID   string `json:"id,omitempty"`
						Name string `json:"name,omitempty"`
					} `json:"content_block"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					p.logger.Debug("anthropic stream parse content_block_start failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				if payload.ContentBlock.Type == "tool_use" {
					acc := &anthropicToolAccum{ID: payload.ContentBlock.ID, Name: payload.ContentBlock.Name}
					toolAccums[payload.Index] = acc
				}
			case "content_block_delta":
				var payload struct {
					Type  string `json:"type"`
					Index int    `json:"index"`
					Delta struct {
						Type        string `json:"type"`
						Text        string `json:"text,omitempty"`
						PartialJSON string `json:"partial_json,omitempty"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					p.logger.Debug("anthropic stream parse content_block_delta failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				switch payload.Delta.Type {
				case "text_delta":
					if payload.Delta.Text == "" {
						return nil
					}
					id := messageID
					if id == "" {
						id = "msg_stream"
					}
					m := model
					if m == "" {
						m = req.Model
					}
					if !emit(StreamChunk{
						ID:    id,
						Model: m,
						Choices: []StreamChoice{{
							Index: 0,
							Delta: Message{Role: "assistant", Content: payload.Delta.Text},
						}},
					}) {
						return io.EOF
					}
				case "input_json_delta":
					if acc, ok := toolAccums[payload.Index]; ok {
						acc.buf.WriteString(payload.Delta.PartialJSON)
					} else {
						// Gracefully handle out-of-order: create placeholder.
						acc := &anthropicToolAccum{}
						acc.buf.WriteString(payload.Delta.PartialJSON)
						toolAccums[payload.Index] = acc
					}
				default:
					// Unknown delta type -> ignore gracefully.
				}
			case "content_block_stop":
				var payload struct {
					Type  string `json:"type"`
					Index int    `json:"index"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					p.logger.Debug("anthropic stream parse content_block_stop failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				if acc, ok := toolAccums[payload.Index]; ok && acc.ID != "" {
					// Assemble accumulated input_json_delta fragments. Empty
					// args become "{}"; malformed (truncated) JSON is emitted
					// raw so the agent loop surfaces invalid-args downstream.
					argsStr := acc.buf.String()
					if argsStr == "" {
						argsStr = "{}"
					}
					id := messageID
					if id == "" {
						id = "msg_stream"
					}
					m := model
					if m == "" {
						m = req.Model
					}
					if !emit(StreamChunk{
						ID:    id,
						Model: m,
						Choices: []StreamChoice{{
							Index: 0,
							Delta: Message{
								Role: "assistant",
								ToolCalls: []ToolCall{{
									ID:   acc.ID,
									Type: "function",
									Function: ToolCallFunction{
										Name:      acc.Name,
										Arguments: argsStr,
									},
								}},
							},
						}},
					}) {
						return io.EOF
					}
					delete(toolAccums, payload.Index)
				}
			case "message_delta":
				var payload struct {
					Type  string `json:"type"`
					Delta struct {
						StopReason   *string `json:"stop_reason,omitempty"`
						StopSequence *string `json:"stop_sequence,omitempty"`
					} `json:"delta"`
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					p.logger.Debug("anthropic stream parse message_delta failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				if payload.Delta.StopReason != nil {
					switch *payload.Delta.StopReason {
					case "tool_use":
						finishReason = "tool_calls"
					case "end_turn":
						finishReason = "stop"
					case "max_tokens":
						finishReason = "length"
					default:
						finishReason = *payload.Delta.StopReason
					}
				}
				if payload.Usage.OutputTokens != 0 {
					usage = &Usage{
						PromptTokens:     inputTokens,
						CompletionTokens: payload.Usage.OutputTokens,
						TotalTokens:      inputTokens + payload.Usage.OutputTokens,
					}
				}
			case "message_stop":
				// Emit final chunk with finish reason and usage.
				if finishReason == "" {
					finishReason = "stop"
				}
				fr := finishReason
				id := messageID
				if id == "" {
					id = "msg_stream"
				}
				m := model
				if m == "" {
					m = req.Model
				}
				ch2 := StreamChunk{
					ID:    id,
					Model: m,
					Choices: []StreamChoice{{
						Index:        0,
						FinishReason: &fr,
						Delta:        Message{Role: "assistant"},
					}},
					Usage: usage,
				}
				_ = emit(ch2)
				return io.EOF
			case "ping":
				// Keepalive, ignore.
			case "":
				// Data without explicit event: try to infer via JSON type field.
				if ev.Data == "" {
					return nil
				}
				var probe struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal([]byte(ev.Data), &probe); err != nil {
					p.logger.Debug("anthropic stream unknown event parse failed", "error", err, "data", logging.Redact(ev.Data))
					return nil
				}
				// Re-dispatch based on probe.Type if it matches known.
				switch probe.Type {
				case "error":
					var errPayload struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(ev.Data), &errPayload); err == nil && errPayload.Error.Message != "" {
						_ = emit(StreamChunk{ID: messageID, Model: model, Error: errPayload.Error.Message})
					} else {
						_ = emit(StreamChunk{ID: messageID, Model: model, Error: ev.Data})
					}
					return io.EOF
				default:
					// Unknown typed event without SSE event field: ignore gracefully.
				}
			default:
				// Unknown event type -> ignore gracefully.
			}
			return nil
		}

		err := parseSSEStream(ctx, resp.Body, handler)
		if err != nil && err != io.EOF && err != context.Canceled && !strings.Contains(err.Error(), "EOF") {
			// Surface transport errors as StreamChunk Error if channel still open.
			// Only if context not canceled and we haven't already emitted terminal.
			select {
			case <-ctx.Done():
			default:
				// Best-effort error chunk.
				select {
				case ch <- StreamChunk{ID: messageID, Model: model, Error: err.Error()}:
				case <-ctx.Done():
				default:
				}
				p.logger.Debug("anthropic stream terminated with error", "error", err)
			}
		}
	}()

	return ch, nil
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
