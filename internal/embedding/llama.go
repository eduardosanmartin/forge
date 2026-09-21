package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LlamaClient talks to a running llama-server's OpenAI-compatible
// /v1/embeddings endpoint (llama.cpp, started with --embeddings --pooling
// mean). It has no knowledge of process lifecycle — starting/stopping the
// server is the daemon's job (internal/cli/daemon.go); this type is just
// the HTTP client, so it can be constructed and health-checked against
// whatever address is already listening.
//
// Chosen empirically, not from specs alone (hojaDeRuta-embeddings-skills.md
// Fase 4): bge-m3 measured a real discrimination gap between relevant and
// irrelevant Spanish queries (~0.08-0.15) against this exact codebase's
// activation threshold, while two smaller multilingual-e5 variants did not
// (near-zero or negative gap) despite scoring numerically higher overall —
// a higher raw score without discrimination is a worse embedding, not a
// better one.
type LlamaClient struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewLlamaClient builds a client for a llama-server already listening at
// baseURL (e.g. "http://127.0.0.1:8901"). model is sent in the request body
// per the OpenAI-compatible schema; llama-server accepts (and mostly
// ignores beyond logging) whatever name is passed here since it only ever
// serves the one model it was started with.
func NewLlamaClient(baseURL, model string) *LlamaClient {
	return &LlamaClient{
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type llamaEmbeddingsRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type llamaEmbeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Generate calls POST {baseURL}/v1/embeddings with text as the sole input
// and returns the resulting vector. A short-timeout context is applied
// internally on top of whatever ctx carries — this call must never hang a
// turn indefinitely just because the embeddings server wedged.
func (c *LlamaClient) Generate(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	body, err := json.Marshal(llamaEmbeddingsRequest{Model: c.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("marshal embeddings request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings request: status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed llamaEmbeddingsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse embeddings response: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings response had no embedding data")
	}
	return parsed.Data[0].Embedding, nil
}

// Healthy makes one real Generate call against trivial text and reports
// whether it succeeded. Used once at daemon startup to decide whether to
// wire this client into embedding.Store at all (see
// NewStoreWithBackend) — deliberately a real call, not just a TCP
// connect, since a connectable-but-broken server (wrong model loaded,
// endpoints misconfigured) is exactly as useless as an unreachable one.
func (c *LlamaClient) Healthy(ctx context.Context) bool {
	_, err := c.Generate(ctx, "health check")
	return err == nil
}

// ProbeDimension makes one real Generate call and returns the length of
// the resulting vector — the embedding dimension this server's currently
// loaded model actually produces. Deliberately measured, not hardcoded to
// one model's known dimension (bge-m3 is 1024): whatever GGUF the config
// points LlamaServerPath/ModelPath at, NewStoreWithBackend gets the right
// dim without needing a per-model lookup table kept in sync by hand. A
// failure here is the same signal as a failed Healthy() — the caller
// should fall back to the hash, not guess a dimension.
func (c *LlamaClient) ProbeDimension(ctx context.Context) (int, error) {
	emb, err := c.Generate(ctx, "dimension probe")
	if err != nil {
		return 0, err
	}
	return len(emb), nil
}
