package client

import (
	"context"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// ManifestExecutor returns an Executor that runs each task goal as one agent turn
// through the daemon (sessionID is the isolated run session). It forwards the
// daemon's tool trace as actual tool-call records so high-sensitivity
// after_task checkpoints can inspect real tool usage, not goal text.
func ManifestExecutor(ctx context.Context, cl *Client, sessionID string) run.Executor {
	return func(ctx context.Context, task run.Task) (run.ExecResult, error) {
		var res daemon.ExecuteTurnResult
		if err := cl.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{
			SessionID:   sessionID,
			UserMessage: task.Goal,
		}, &res); err != nil {
			return run.ExecResult{}, fmt.Errorf("execute task %s: %w", task.ID, err)
		}
		tokens := 0
		iters := 1
		if res.Usage != nil {
			tokens = res.Usage.TotalTokens
		}
		// Estimate iterations from tool trace length + 1.
		if len(res.ToolTrace) > 0 {
			iters = len(res.ToolTrace) + 1
		}
		toolCalls := make([]string, 0, len(res.ToolTrace))
		for _, tr := range res.ToolTrace {
			toolCalls = append(toolCalls, tr.Name)
		}
		// Non-nil slice (even if empty) signals "records available" — an empty
		// slice means no tools were executed, which is distinct from nil
		// (unavailable) used by legacy mocks.
		return run.ExecResult{Tokens: tokens, Iterations: iters, ToolCalls: toolCalls}, nil
	}
}
