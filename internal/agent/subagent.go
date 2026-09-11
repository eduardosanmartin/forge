// Package agent implements subagent spawning with bounded context and branch isolation.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/store"
)

// ChildSpec bounds a child turn (RF-1.3).
type ChildSpec struct {
	Task          string
	MaxIterations int // 0 = parent default, clamped to parent max never wider
	TokenBudget   int // 0 = no explicit cap; recorded in metadata for observability
	FileBudget    string
}

// ChildResult summarizes one isolated child turn.
type ChildResult struct {
	ChildSessionID  string
	ParentSessionID string
	Task            string
	Success         bool
	Summary         string
	Error           string
	Metrics         TurnMetrics
	Messages        []store.Message // child's new messages (user + assistant + tool)
}

const maxSubagentDepth = 3

// branchStore is the optional store capability for subagents (branches are the natural container).
type branchStore interface {
	BranchSession(ctx context.Context, sourceID string, atSeq int, metadata map[string]any) (store.Session, error)
}

// SpawnChild runs a bounded child turn isolated in a branched session.
// The child inherits the session's permission floor (same Engine, same registry)
// so it is never wider than the parent; narrowing is structural by reusing
// the same Engine/registry. Child transcripts live in the branch; the parent
// receives only the summarized result (returned to the caller to inject as a
// tool result). Bounded context: task + file/token budget + max iterations.
// Sequential multi-child is inherent; true parallelism is blocked by the
// single SQLite connection and single LLM provider queue (RNF-1.5) — see note
// in loop.go and daemon wiring.
func (a *Agent) SpawnChild(ctx context.Context, parentSessionID string, spec ChildSpec) (ChildResult, error) {
	if strings.TrimSpace(spec.Task) == "" {
		return ChildResult{}, fmt.Errorf("spawn child: task must not be empty")
	}
	if spec.MaxIterations < 0 {
		return ChildResult{}, fmt.Errorf("spawn child: max_iterations must be >= 0")
	}
	if spec.TokenBudget < 0 {
		return ChildResult{}, fmt.Errorf("spawn child: token_budget must be >= 0")
	}

	// Validate parent session exists and depth limit.
	parent, err := a.store.GetSession(ctx, parentSessionID)
	if err != nil {
		return ChildResult{}, fmt.Errorf("spawn child: parent session not found: %w", err)
	}
	if parent.Metadata != nil {
		if v, ok := parent.Metadata["subagent_depth"]; ok {
			if depth, ok := toInt(v); ok && depth >= maxSubagentDepth {
				return ChildResult{}, fmt.Errorf("spawn child: max subagent depth %d reached (parent %s depth %d)", maxSubagentDepth, parentSessionID, depth)
			}
		}
	}
	bs, ok := a.store.(branchStore)
	if !ok {
		return ChildResult{}, fmt.Errorf("spawn child: store does not support branching")
	}

	// Clamp child max iterations to parent ceiling (never wider).
	childMax := a.maxIterations
	if spec.MaxIterations > 0 {
		if spec.MaxIterations < childMax {
			childMax = spec.MaxIterations
		}
		// If spec requests more than parent max, keep parent max (clamped).
	}
	if childMax <= 0 {
		childMax = a.maxIterations
	}

	parentDepth := 0
	if parent.Metadata != nil {
		if v, ok := parent.Metadata["subagent_depth"]; ok {
			if d, ok := toInt(v); ok {
				parentDepth = d
			}
		}
	}
	childDepth := parentDepth + 1

	meta := map[string]any{
		"subagent":               true,
		"subagent_parent":        parentSessionID,
		"subagent_task":          spec.Task,
		"subagent_depth":         childDepth,
		"subagent_max_iterations": childMax,
	}
	if spec.TokenBudget > 0 {
		meta["subagent_token_budget"] = spec.TokenBudget
	}
	if spec.FileBudget != "" {
		meta["subagent_file_budget"] = spec.FileBudget
	}

	branched, err := bs.BranchSession(ctx, parentSessionID, 0, meta)
	if err != nil {
		return ChildResult{}, fmt.Errorf("spawn child: branch: %w", err)
	}

	// Run child with bounded iteration limit via a shallow agent clone.
	childAgent := &Agent{
		cfg:            a.cfg,
		ctxAssembler:   a.ctxAssembler,
		llmReg:         a.llmReg,
		toolsReg:       a.toolsReg,
		permsEngine:    a.permsEngine,
		store:          a.store,
		logger:         a.logger,
		maxIterations:  childMax,
		maxTurnSeconds: a.maxTurnSeconds,
	}
	// Preserve V1 deps wired on the parent assembler.
	childResult, execErr := childAgent.ExecuteTurn(ctx, branched.ID, spec.Task)

	cr := ChildResult{
		ChildSessionID:  branched.ID,
		ParentSessionID: parentSessionID,
		Task:            spec.Task,
		Messages:        childResult.Messages,
		Metrics:         childResult.Metrics,
	}
	if execErr != nil {
		cr.Success = false
		cr.Error = execErr.Error()
		// Still provide summary from whatever messages were produced.
		cr.Summary = summarizeChildMessages(childResult.Messages, execErr.Error())
		return cr, nil
	}
	if childResult.Halted {
		cr.Success = false
		if childResult.Error != nil {
			cr.Error = childResult.Error.Error()
		}
		cr.Summary = summarizeChildMessages(childResult.Messages, cr.Error)
		return cr, nil
	}
	cr.Success = true
	cr.Summary = summarizeChildMessages(childResult.Messages, "")
	return cr, nil
}

// summarizeChildMessages extracts the last assistant content or falls back to error.
func summarizeChildMessages(msgs []store.Message, fallbackErr string) string {
	var lastAssistant string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && strings.TrimSpace(msgs[i].Content) != "" {
			lastAssistant = msgs[i].Content
			break
		}
	}
	if lastAssistant != "" {
		const maxSummary = 4000
		if len(lastAssistant) > maxSummary {
			return lastAssistant[:maxSummary] + "\n...[truncated]"
		}
		return lastAssistant
	}
	if fallbackErr != "" {
		return fallbackErr
	}
	if len(msgs) == 0 {
		return "(no output)"
	}
	// Fallback to last message content.
	return msgs[len(msgs)-1].Content
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}
