package cli

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/routing"
)

// summaryMaxTokens bounds one summary completion (prompts ask for at most
// 120-160 words).
const summaryMaxTokens = 320

// llmSummaries returns the LLM summary hierarchy when a provider declares
// a model for the "cheap" role, else nil. The table is created here so a
// failure only disables the feature.
func llmSummaries(ctx context.Context, db *sql.DB, reg *llm.Registry, compactor *compaction.Compactor) (*compaction.Hierarchy, string) {
	provider := reg.ProviderForRole(routing.RoleCheap)
	if provider == nil {
		return nil, ""
	}
	model := reg.GetRouter().ModelForRole(routing.RoleCheap)
	if model == "" {
		return nil, ""
	}
	if err := compaction.CreateSummaryTable(ctx, db); err != nil {
		slog.Default().Warn("compaction: summary table unavailable, LLM summaries disabled", "error", err)
		return nil, ""
	}
	return compaction.NewHierarchy(compaction.NewSQLSummaryCache(db), summarizeWith(provider, model), model, 4, compactor.SummarizeBlock), model
}

// summarizeWith returns a SummarizeFunc over one provider and model.
func summarizeWith(provider llm.Provider, model string) compaction.SummarizeFunc {
	return func(ctx context.Context, prompt string) (string, error) {
		temp, maxTokens := 0.0, summaryMaxTokens
		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Model:       model,
			Messages:    []llm.Message{{Role: "user", Content: prompt}},
			Temperature: &temp,
			MaxTokens:   &maxTokens,
		})
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", errors.New("summary model returned no choices")
		}
		return resp.Choices[0].Message.Content, nil
	}
}
