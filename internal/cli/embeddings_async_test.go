package cli

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/embedding"
)

// N2: starting the daemon must not wait for the embeddings backend.
func TestStartEmbeddingsAsyncDoesNotBlock(t *testing.T) {
	orig := embeddingsStarter
	defer func() { embeddingsStarter = orig }()
	embeddingsStarter = func(ctx context.Context, cfg config.EmbeddingsConfig, logger *slog.Logger) (*embedding.LlamaClient, int, func()) {
		time.Sleep(500 * time.Millisecond) // a slow llama-server start
		return embedding.NewLlamaClient("http://127.0.0.1:1", "m"), 1024, func() {}
	}
	ready := make(chan int, 1)
	t0 := time.Now()
	cleanup := startEmbeddingsAsync(context.Background(), config.EmbeddingsConfig{Enabled: true}, slog.New(slog.DiscardHandler),
		func(_ *embedding.LlamaClient, dim int) { ready <- dim })
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("startEmbeddingsAsync blocked for %v", d)
	}
	select {
	case dim := <-ready:
		if dim != 1024 {
			t.Fatalf("onReady dim = %d", dim)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("onReady never called")
	}
	cleanup()
}
