package cli

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"

	_ "modernc.org/sqlite"
)

type recordingSummaryProvider struct{ req llm.ChatRequest }

func (p *recordingSummaryProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.req = req
	return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "summary"}}}}, nil
}
func (p *recordingSummaryProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}
func (p *recordingSummaryProvider) ListModels() ([]string, error) { return nil, nil }
func (p *recordingSummaryProvider) Close() error                  { return nil }

func TestSummarizeWithIsDeterministicAndBounded(t *testing.T) {
	p := &recordingSummaryProvider{}
	got, err := summarizeWith(p, "qwen2.5-coder:1.5b")(context.Background(), "prompt")
	if err != nil || got != "summary" {
		t.Fatalf("got %q, %v", got, err)
	}
	if p.req.Model != "qwen2.5-coder:1.5b" || p.req.Temperature == nil || *p.req.Temperature != 0 ||
		p.req.MaxTokens == nil || *p.req.MaxTokens != summaryMaxTokens || len(p.req.Tools) != 0 {
		t.Fatalf("unexpected request: %+v", p.req)
	}
}

// The generation model is never used for background summaries: without a
// "cheap" role the feature stays off.
func TestLLMSummariesRequireACheapRole(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	compactor := compaction.NewCompactor(compaction.Config{})

	for _, tc := range []struct {
		roles map[string]string
		want  bool
	}{
		{map[string]string{"generation": "qwen2.5-coder:7b"}, false},
		{map[string]string{"generation": "qwen2.5-coder:7b", "cheap": "qwen2.5-coder:1.5b"}, true},
	} {
		cfg := config.Defaults()
		cfg.DefaultProvider = "ollama"
		cfg.Providers = map[string]config.Provider{"ollama": {
			Kind: "openai-compatible", BaseURL: "http://127.0.0.1:11434/v1",
			Models: []string{"qwen2.5-coder:7b"}, ModelRoles: tc.roles,
		}}
		reg, err := llm.New(cfg, []string{"127.0.0.1"}, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		h, model := llmSummaries(context.Background(), db, reg, compactor)
		_ = reg.Close()
		if (h != nil) != tc.want {
			t.Fatalf("roles %v: enabled = %v, want %v", tc.roles, h != nil, tc.want)
		}
		if tc.want && model != "qwen2.5-coder:1.5b" {
			t.Fatalf("summary model = %q, want the cheap one", model)
		}
	}
}
