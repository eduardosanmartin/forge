// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"time"

	"github.com/eduardosanmartin/forge/internal/store"
)

// TurnResult represents the outcome of a single agent turn.
type TurnResult struct {
	Messages []store.Message // all new messages appended this turn (user + assistant + tool results)
	Metrics  TurnMetrics
	Halted   bool
	Error    error
}

// TurnMetrics captures timing and token metrics for a single turn.
type TurnMetrics struct {
	StartTime         time.Time
	EndTime           time.Time
	HarnessOverheadMs int64 // time spent in agent loop NOT waiting for LLM
	LLMTimeMs         int64 // time waiting for LLM response(s)
	TotalTokens       int   // prompt + completion
	PromptTokens      int
	CompletionTokens  int
	ToolCallCount     int
	IterationCount    int
}

// DurationMs returns the total turn duration in milliseconds.
func (m TurnMetrics) DurationMs() int64 {
	return m.EndTime.Sub(m.StartTime).Milliseconds()
}
