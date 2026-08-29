package compaction

import (
	"strings"
)

// Turn represents a single conversation turn.
type Turn struct {
	Role    string
	Content string
	Tokens  int
	Anchor  bool   // If true, never compressed/removed
	Summary string // For summarized turns
}

// Config holds compaction configuration.
type Config struct {
	SummaryModel    string  // Model for summarization (small/fast)
	GenerationModel string  // Model for generation (large/capable)
	AnchorThreshold float64 // Score threshold to auto-mark as anchor
}

// CompactionStats holds statistics about compaction.
type CompactionStats struct {
	OriginalTurns    int
	CompactedTurns   int
	OriginalTokens   int
	CompactedTokens  int
	AnchorsPreserved int
	SummariesCreated int
}

// Compactor handles hierarchical compaction of conversation turns.
type Compactor struct {
	summaryModel    string
	generationModel string
	anchorThreshold float64
}

// NewCompactor creates a new compactor with the given config.
func NewCompactor(cfg Config) *Compactor {
	return &Compactor{
		summaryModel:    cfg.SummaryModel,
		generationModel: cfg.GenerationModel,
		anchorThreshold: cfg.AnchorThreshold,
	}
}

// Compact performs hierarchical compaction on turns.
// Returns compacted turns, stats, and error.
func (c *Compactor) Compact(turns []Turn) ([]Turn, CompactionStats, error) {
	stats := CompactionStats{
		OriginalTurns:  len(turns),
		OriginalTokens: 0,
	}

	for _, t := range turns {
		stats.OriginalTokens += t.Tokens
	}

	if len(turns) == 0 {
		return []Turn{}, stats, nil
	}

	// Separate anchors from regular turns
	var anchors []Turn
	var regular []Turn
	for _, t := range turns {
		if t.Anchor {
			anchors = append(anchors, t)
		} else {
			regular = append(regular, t)
		}
	}
	stats.AnchorsPreserved = len(anchors)

	// If session is small, return as-is (maybe with light summarization)
	if len(regular) <= 10 && stats.OriginalTokens < 1000 {
		compacted := append(anchors, regular...)
		stats.CompactedTurns = len(compacted)
		stats.CompactedTokens = stats.OriginalTokens
		return compacted, stats, nil
	}

	// Hierarchical compaction:
	// 1. Split into chunks
	// 2. Summarize each chunk (using summary model)
	// 3. Keep recent turns verbatim
	// 4. Preserve all anchors

	chunkSize := 20 // turns per summary
	var compacted []Turn
	summariesCreated := 0

	// Process older turns in chunks, summarize each
	keepRecent := 10 // keep last 10 turns verbatim
	if len(regular) > keepRecent {
		older := regular[:len(regular)-keepRecent]
		recent := regular[len(regular)-keepRecent:]

		// Summarize older turns in chunks
		for i := 0; i < len(older); i += chunkSize {
			end := i + chunkSize
			if end > len(older) {
				end = len(older)
			}
			chunk := older[i:end]

			// Create summary turn
			summary := c.createSummary(chunk)
			compacted = append(compacted, Turn{
				Role:    "system",
				Content: summary,
				Tokens:  len(summary) / 4, // rough estimate
				Summary: summary,
			})
			summariesCreated++
		}

		// Add recent turns verbatim
		compacted = append(compacted, recent...)
	} else {
		compacted = append(compacted, regular...)
	}

	// Add all anchors (always preserved)
	compacted = append(compacted, anchors...)

	stats.CompactedTurns = len(compacted)
	stats.SummariesCreated = summariesCreated

	// Estimate compacted tokens
	for _, t := range compacted {
		stats.CompactedTokens += t.Tokens
	}

	return compacted, stats, nil
}

// createSummary creates a summary of a chunk of turns.
// In production, this would call the summary model (small/fast).
func (c *Compactor) createSummary(turns []Turn) string {
	if len(turns) == 0 {
		return ""
	}

	var parts []string
	for _, t := range turns {
		// Truncate content for summary
		content := t.Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		parts = append(parts, t.Role+": "+content)
	}
	return "RESUMEN: " + strings.Join(parts, "; ")
}

// GetSummaryModel returns the model used for summarization.
func (c *Compactor) GetSummaryModel() string {
	return c.summaryModel
}

// GetGenerationModel returns the model used for generation.
func (c *Compactor) GetGenerationModel() string {
	return c.generationModel
}
