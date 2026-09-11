package client

import (
	"context"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// ManifestExecutor returns an Executor that runs each task goal as one agent turn
// through the daemon (sessionID is the isolated run session).
func ManifestExecutor(ctx context.Context, cl *Client, sessionID string) run.Executor {
	return func(ctx context.Context, task run.Task) (int, int, error) {
		var res daemon.ExecuteTurnResult
		if err := cl.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{
			SessionID:   sessionID,
			UserMessage: task.Goal,
		}, &res); err != nil {
			return 0, 0, fmt.Errorf("execute task %s: %w", task.ID, err)
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
		return tokens, iters, nil
	}
}
