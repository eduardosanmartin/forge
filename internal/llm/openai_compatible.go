// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter and a model registry supporting hot-swap.
package llm

import (
	"bufio"
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

const forgeUserAgent = "forge/0.0.0-dev"

// OpenAICompatibleProvider implements Provider for OpenAI-compatible endpoints (e.g., Ollama, OpenCode Zen, OpenRouter).
type OpenAICompatibleProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	models     []string
	modelsMu   sync.RWMutex
	closed     bool
	closedMu   sync.Mutex
}

// NewOpenAICompatibleProvider creates a new OpenAI-compatible provider.
// allowedHosts enforces network egress allowlist (RNF-4.9): baseURL host:port must
// be in the list (exact match). Empty allowlist = deny all. Returns error if not allowed.
func NewOpenAICompatibleProvider(baseURL, apiKey string, allowedHosts []string, logger *slog.Logger) (*OpenAICompatibleProvider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// API key: explicit config value wins; fall back to env for secret safety.
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
	}

	// Validate allowlist before creating the provider
	if err := validateAllowlist(baseURL, allowedHosts); err != nil {
		return nil, err
	}

	// Ensure baseURL has a path; default to /v1 for OpenAI-compatible
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1"
		baseURL = parsed.String()
	}

	client := &http.Client{
		// Generous end-to-end bound per LLM call. Local 7B-class models on
		// consumer hardware routinely need several minutes for one cold
		// non-streaming completion (model load + prefill + long generation);
		// a tighter cap aborts healthy turns of exactly the kind the spec §6
		// exit criterion demands. Keep internal/client readIdleTimeout ABOVE
		// this value so clients never idle out while waiting on a frame that
		// can legitimately take this long.
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 30 * time.Second,
			}).DialContext,
			DisableKeepAlives:  false,
			DisableCompression: false,
		},
		// Security: do not follow redirects
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	p := &OpenAICompatibleProvider{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: client,
		logger:     logger,
	}

	// Fetch models at startup
	if err := p.RefreshModels(); err != nil {
		logger.Warn("failed to fetch models at startup", "error", err)
		// Don't fail construction; models can be refreshed later
	}

	return p, nil
}

// SetRequestTimeout overrides the per-request HTTP timeout (default 15
// minutes, set above at construction). Ignored when d <= 0 — the registry
// calls this once, right after construction and before any request, with
// config.Provider.RequestTimeoutSeconds (previously every provider was
// stuck with the same fixed 15-minute timeout regardless of whether it
// fronted a fast local model or a legitimately slow remote one).
func (p *OpenAICompatibleProvider) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		p.httpClient.Timeout = d
	}
}

// validateAllowlist checks if the baseURL host is in the allowedHosts list.
// Empty allowlist denies all (RNF-4.9). Matching rules:
//   - an entry WITH a port ("127.0.0.1:11434") requires an exact host:port match;
//   - a portless entry ("127.0.0.1") matches the URL's hostname on any port.
//
// This keeps shipped defaults usable (config lists bare hostnames) without
// weakening the deny-by-default posture: anything not matched is still denied.
func validateAllowlist(baseURL string, allowedHosts []string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url for allowlist check: %w", err)
	}
	hostPort := parsed.Host
	if hostPort == "" {
		return errors.New("base_url missing host")
	}
	hostname := parsed.Hostname()

	if len(allowedHosts) == 0 {
		return fmt.Errorf("network egress denied: empty allowlist, host %q not allowed", hostPort)
	}

	for _, allowed := range allowedHosts {
		if allowed == hostPort {
			return nil
		}
		if !strings.Contains(allowed, ":") && allowed == hostname {
			return nil
		}
	}

	return fmt.Errorf("network egress denied: host %q not in allowlist %v", hostPort, allowedHosts)
}

// RefreshModels re-fetches the live model list from /models (OpenAI-compatible
// endpoint) — not the config's declared list, the provider's own catalog.
// The baseURL is guaranteed to have /v1 path by the constructor. Exported so
// a provider switch (daemon.switch_provider) can force a fresh list instead
// of serving whatever was cached at daemon startup.
func (p *OpenAICompatibleProvider) RefreshModels() error {
	models, err := p.fetchModels(p.baseURL + "/models")
	if err != nil {
		p.logger.Debug("fetch /models failed", "error", err)
		return err
	}

	// Deduplicate
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

func (p *OpenAICompatibleProvider) fetchModels(endpoint string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", forgeUserAgent)
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
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
			ID   string `json:"id"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"` // OpenAI compat uses "data"
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range result.Models {
		if m.Name != "" {
			models = append(models, m.Name)
		} else if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	for _, d := range result.Data {
		if d.ID != "" {
			models = append(models, d.ID)
		}
	}
	return models, nil
}

// Chat implements Provider.Chat for non-streaming requests.
func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	p.closedMu.Lock()
	if p.closed {
		p.closedMu.Unlock()
		return ChatResponse{}, errors.New("provider closed")
	}
	p.closedMu.Unlock()

	body, err := json.Marshal(p.buildRequestBody(req))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", forgeUserAgent)
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if req.SessionID != "" {
		httpReq.Header.Set("x-opencode-session", req.SessionID)
	}

	p.logger.Debug("chat request", "endpoint", endpoint, "body", logging.Redact(string(body)))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, p.mapError(err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}

	p.logger.Debug("chat response", "status", resp.StatusCode, "body", logging.Redact(string(respBody)))

	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, p.mapHTTPError(resp.StatusCode, respBody, resp.Header)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return ChatResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}

	return chatResp, nil
}

// ChatStream implements Provider.ChatStream for streaming requests.
func (p *OpenAICompatibleProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	p.closedMu.Lock()
	if p.closed {
		p.closedMu.Unlock()
		return nil, errors.New("provider closed")
	}
	p.closedMu.Unlock()

	streamReq := req
	streamReq.Stream = true

	body, err := json.Marshal(p.buildRequestBody(streamReq))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", forgeUserAgent)
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if req.SessionID != "" {
		httpReq.Header.Set("x-opencode-session", req.SessionID)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	p.logger.Debug("chat stream request", "endpoint", endpoint, "body", logging.Redact(string(body)))

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, p.mapError(err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, p.mapHTTPError(resp.StatusCode, respBody, resp.Header)
	}

	ch := make(chan StreamChunk, 16)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Bytes()
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			// SSE format: "data: {...}" or "data: [DONE]"
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if bytes.Equal(data, []byte("[DONE]")) {
				return
			}

			var chunk StreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				p.logger.Debug("stream parse error", "error", err, "data", logging.Redact(string(data)))
				continue
			}

			select {
			case ch <- chunk:
			case <-ctx.Done():
				return
			}
		}

		if err := scanner.Err(); err != nil {
			p.logger.Debug("stream scanner error", "error", err)
		}
	}()

	return ch, nil
}

func (p *OpenAICompatibleProvider) buildRequestBody(req ChatRequest) map[string]any {
	body := map[string]any{
		"model":    req.Model,
		"messages": p.messagesToAPI(req.Messages),
		"stream":   req.Stream,
	}

	if len(req.Tools) > 0 {
		body["tools"] = p.toolsToAPI(req.Tools)
		// An empty tool_choice is invalid for strict OpenAI-compatible endpoints
		// (e.g. OpenCode Zen / Nemotron) and yields HTTP 400. Default to "auto"
		// so the model may decide whether to call a tool.
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		} else {
			body["tool_choice"] = "auto"
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}

	return body
}

func (p *OpenAICompatibleProvider) messagesToAPI(msgs []Message) []map[string]any {
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		msg := map[string]any{
			"role":    m.Role,
			"content": m.Content,
		}
		// "name" is only ever populated on a "tool" role message in practice
		// (the tool's name, for storage/audit — see internal/agent/loop.go).
		// It's a leftover of the pre-tool_calls "function" message shape:
		// the current tool-calling protocol identifies a tool result solely
		// by tool_call_id, and at least one real backend (OpenCode Zen
		// "Console Go") rejects "name" on a "tool" message outright ("name"
		// is not supported by this endpoint). Other backends likely just
		// ignore the extra field, which is why this went unnoticed until a
		// stricter validator hit it.
		if m.Name != "" && m.Role != "tool" {
			msg["name"] = m.Name
		}
		if len(m.ToolCalls) > 0 {
			toolCalls := make([]map[string]any, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				toolCalls[j] = map[string]any{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				}
			}
			msg["tool_calls"] = toolCalls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		out[i] = msg
	}
	return out
}

func (p *OpenAICompatibleProvider) toolsToAPI(tools []ToolDef) []map[string]any {
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		out[i] = map[string]any{
			"type": t.Type,
			"function": map[string]any{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			},
		}
	}
	return out
}

// ListModels implements Provider.ListModels.
func (p *OpenAICompatibleProvider) ListModels() ([]string, error) {
	p.modelsMu.RLock()
	defer p.modelsMu.RUnlock()
	// Return a copy
	models := make([]string, len(p.models))
	copy(models, p.models)
	return models, nil
}

// Close implements Provider.Close.
func (p *OpenAICompatibleProvider) Close() error {
	p.closedMu.Lock()
	defer p.closedMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.httpClient.CloseIdleConnections()
	return nil
}

// mapError maps network/transport errors to typed errors. Timeouts and
// connection failures are wrapped as RetryableError (StatusCode 0 — no HTTP
// response was ever received): they're exactly the transient case a
// fallback chain exists for, as opposed to e.g. a malformed request that
// would fail identically against any other model too.
func (p *OpenAICompatibleProvider) mapError(err error) error {
	var netErr *url.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return &RetryableError{Err: fmt.Errorf("request timeout: %w", err)}
		}
		return &RetryableError{Err: fmt.Errorf("connection error: %w", err)}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &RetryableError{Err: fmt.Errorf("request deadline exceeded: %w", err)}
	}
	return fmt.Errorf("request failed: %w", err)
}

// mapHTTPError maps HTTP error status codes to typed errors. 429 (rate
// limited) and 502/503/504 (upstream unavailable) are wrapped as
// RetryableError — see that type's doc comment for why the rest (400/401/
// 403/404/500) are deliberately NOT: retrying an unauthorized or malformed
// request against a different model wastes an attempt on a failure a model
// swap can't fix.
func (p *OpenAICompatibleProvider) mapHTTPError(statusCode int, body []byte, headers http.Header) error {
	bodyStr := logging.Redact(string(body))
	switch statusCode {
	case http.StatusNotFound:
		return fmt.Errorf("model not found (404): %s", bodyStr)
	case http.StatusTooManyRequests:
		return &RetryableError{
			StatusCode: statusCode,
			RetryAfter: parseRetryAfter(headers),
			Err:        fmt.Errorf("rate limited (429): %s", bodyStr),
		}
	case http.StatusUnauthorized:
		return fmt.Errorf("unauthorized (401): %s", bodyStr)
	case http.StatusForbidden:
		return fmt.Errorf("forbidden (403): %s", bodyStr)
	case http.StatusBadRequest:
		return fmt.Errorf("bad request (400): %s", bodyStr)
	case http.StatusInternalServerError:
		return fmt.Errorf("server error (500): %s", bodyStr)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return &RetryableError{
			StatusCode: statusCode,
			RetryAfter: parseRetryAfter(headers),
			Err:        fmt.Errorf("upstream unavailable (%d): %s", statusCode, bodyStr),
		}
	default:
		return fmt.Errorf("HTTP %d: %s", statusCode, bodyStr)
	}
}
