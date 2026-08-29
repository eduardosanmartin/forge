package compaction

import (
	"testing"
)

func TestCompactorBasic(t *testing.T) {
	c := NewCompactor(Config{
		SummaryModel:    "test-model-small",
		GenerationModel: "test-model-large",
		AnchorThreshold: 0.8,
	})

	turns := make([]Turn, 0, 120)
	for i := 0; i < 120; i++ {
		turns = append(turns, Turn{
			Role:    "user",
			Content: "turn content " + string(rune(i)),
			Tokens:  50,
		})
	}

	turns[50] = Turn{
		Role:    "user",
		Content: "ANCLA: decisión importante usar SQLite",
		Tokens:  30,
		Anchor:  true,
	}

	compacted, stats, err := c.Compact(turns)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if stats.OriginalTokens <= stats.CompactedTokens {
		t.Errorf("expected reduction, got original=%d compacted=%d", stats.OriginalTokens, stats.CompactedTokens)
	}
	reduction := float64(stats.OriginalTokens-stats.CompactedTokens) / float64(stats.OriginalTokens)
	if reduction < 0.4 {
		t.Errorf("expected ≥40%% reduction, got %.1f%%", reduction*100)
	}

	anchorFound := false
	for _, t := range compacted {
		if t.Anchor && t.Content == "ANCLA: decisión importante usar SQLite" {
			anchorFound = true
			break
		}
	}
	if !anchorFound {
		t.Error("anchor not preserved in compacted output")
	}

	if len(compacted) >= len(turns) {
		t.Errorf("compacted should be smaller than original")
	}
}

func TestCompactorPreservesAnchors(t *testing.T) {
	c := NewCompactor(Config{
		SummaryModel:    "test",
		GenerationModel: "test",
		AnchorThreshold: 0.8,
	})

	turns := []Turn{
		{Role: "user", Content: "normal turn 1", Tokens: 10},
		{Role: "user", Content: "ANCLA: hecho clave", Tokens: 10, Anchor: true},
		{Role: "user", Content: "normal turn 2", Tokens: 10},
		{Role: "user", Content: "otro ANCLA: configuración", Tokens: 10, Anchor: true},
		{Role: "user", Content: "normal turn 3", Tokens: 10},
	}

	compacted, _, err := c.Compact(turns)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	anchorCount := 0
	for _, t := range compacted {
		if t.Anchor {
			anchorCount++
		}
	}
	if anchorCount != 2 {
		t.Errorf("expected 2 anchors preserved, got %d", anchorCount)
	}
}

func TestCompactorEmptyInput(t *testing.T) {
	c := NewCompactor(Config{})
	_, stats, err := c.Compact([]Turn{})
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if stats.OriginalTokens != 0 {
		t.Errorf("expected 0 original tokens, got %d", stats.OriginalTokens)
	}
}

func TestCompactorSmallSessionNoCompaction(t *testing.T) {
	c := NewCompactor(Config{
		SummaryModel:    "test",
		GenerationModel: "test",
	})

	turns := []Turn{
		{Role: "user", Content: "hola", Tokens: 5},
		{Role: "assistant", Content: "hola!", Tokens: 5},
	}

	_, stats, err := c.Compact(turns)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if stats.OriginalTokens == 0 {
		t.Error("expected original tokens > 0")
	}
}

func TestCompactorModelSelection(t *testing.T) {
	c := NewCompactor(Config{
		SummaryModel:    "qwen2.5-coder:1.5b",
		GenerationModel: "qwen2.5-coder:7b",
	})

	if c.GetSummaryModel() != "qwen2.5-coder:1.5b" {
		t.Errorf("expected summary model 1.5b, got %s", c.GetSummaryModel())
	}
	if c.GetGenerationModel() != "qwen2.5-coder:7b" {
		t.Errorf("expected generation model 7b, got %s", c.GetGenerationModel())
	}
}
