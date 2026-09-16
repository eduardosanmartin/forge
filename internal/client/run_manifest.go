package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// ManifestExecutor returns an Executor that runs each task goal as one agent turn
// through the daemon (sessionID is the isolated run session). It forwards the
// daemon's tool trace as actual tool-call records so high-sensitivity
// after_task checkpoints can inspect real tool usage, not goal text.
// task.ModelHint (when set — by hand or by the decomposition step, §5.2)
// travels as ExecuteTurnParams.ModelHint: the daemon resolves it through the
// registry's ModelRouter into a concrete per-turn model override
// (sugerenciasDeClaude.md §5.6) — this is the piece that was missing before:
// the field existed on Task but nothing consumed it.
//
// onToolTick, when non-nil, is called for every daemon.MethodToolCallEvent
// notification this session receives while the task's turn is in flight —
// live per-tool-call progress for a blocking call that can otherwise run
// silently for minutes. Nil disables the subscription entirely (no
// overhead). Delivery is best-effort: cl.Events() never subscribes this
// connection to anything (Transport.Subscribe has no caller anywhere in the
// daemon — confirmed dead code), which means an unsubscribed connection
// receives every broadcast notification by default (see
// Transport.dispatchNotification's `len(cc.subscriptions) == 0` branch) —
// so no explicit subscribe step is needed here, just session-ID filtering
// on the receiving end.
func ManifestExecutor(ctx context.Context, cl *Client, sessionID string, onToolTick func(daemon.ToolCallEventPayload)) run.Executor {
	return func(ctx context.Context, task run.Task) (run.ExecResult, error) {
		if onToolTick != nil {
			evCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if events, evErr := cl.Events(evCtx); evErr == nil {
				go func() {
					for {
						select {
						case <-evCtx.Done():
							return
						case notif, ok := <-events:
							if !ok {
								return
							}
							if notif.Method != daemon.MethodToolCallEvent {
								continue
							}
							var payload daemon.ToolCallEventPayload
							if json.Unmarshal(notif.Params, &payload) != nil || payload.SessionID != sessionID {
								continue
							}
							onToolTick(payload)
						}
					}
				}()
			}
		}
		var res daemon.ExecuteTurnResult
		if err := cl.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{
			SessionID:   sessionID,
			UserMessage: task.Goal,
			ModelHint:   task.ModelHint,
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

// ManifestDecomposer returns a run.Decomposer that asks the daemon's default
// model to break goal+spec into a task list (run.BuildDecompositionPrompt),
// through a dedicated, throwaway session created fresh on every call — kept
// separate from the run's own session so decomposition chatter (and any
// tool calls the model makes while exploring the spec) never becomes part
// of the context every subsequent task turn sees.
func ManifestDecomposer(cl *Client) run.Decomposer {
	return func(ctx context.Context, goal, spec string) ([]run.Task, error) {
		var created daemon.SessionResult
		if err := cl.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{
			// no_tools (internal/daemon/session_mgr.go): forces a plain-text
			// answer for every turn in this session — the decomposition
			// prompt already asks the model not to call tools, but that's
			// prompt compliance only; this makes it a hard technical
			// constraint (empty tools array in the ChatRequest) instead.
			// Found necessary in practice: a real decomposition call against
			// this very repo exhausted max_iterations exploring the
			// filesystem instead of answering, despite the prompt asking it
			// not to.
			Metadata: map[string]any{"source": "run_manifest_decompose", "no_tools": true},
		}, &created); err != nil {
			return nil, fmt.Errorf("create decomposition session: %w", err)
		}

		var res daemon.ExecuteTurnResult
		if err := cl.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{
			SessionID:   created.ID,
			UserMessage: run.BuildDecompositionPrompt(goal, spec),
		}, &res); err != nil {
			return nil, fmt.Errorf("decomposition turn: %w", err)
		}

		tasks, err := run.ParseDecomposedTasks(res.FinalContent)
		if err != nil {
			return nil, err
		}
		return tasks, nil
	}
}
