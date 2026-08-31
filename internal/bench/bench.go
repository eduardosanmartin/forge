// Package bench implements the RNF-10 / RNF-2.3 context-token reduction
// benchmark. It drives the REAL ContextAssembler over the REAL SQLite store
// and the REAL v1 packages (retrieval, compaction, anchoring) with a
// deterministic scripted conversation, and compares the prompt tokens of two
// arms over an identical turn sequence:
//
//	baseline arm: "full history" — maxHistoryTurns set huge, v1 flags off,
//	              no v1 dependencies. This is the naive approach RNF-2.3
//	              measures against.
//	v1 arm:       production default window (10 turns), v1 flags on
//	              (retrieval + compaction + anchoring), real v1 dependencies
//	              wired exactly like the daemon does in cli/daemon.go
//	              runServe.
//
// The reduction is a property of the REQUEST PAYLOAD (prompt tokens per
// turn), not of the model, so the bench is deterministic and model-free: no
// LLM is ever called. Scripted completions are not modeled, and latency is
// not measured (RNF-10.1 requires a live model and is out of scope for this
// offline pass).
package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// DefaultReductionThresholdPct is the RNF-2.3 exit criterion: at least a 40%
// reduction in context tokens versus the naive full-history approach.
const DefaultReductionThresholdPct = 40.0

// Injection header prefixes emitted by agent.ContextAssembler. These mirror
// unexported literals in the agent package; if the assembler changes its
// injection headers, update these constants.
const (
	retrievalInjectionPrefix  = "RELEVANT CONTEXT (v1):"
	compactionInjectionPrefix = "COMPACTED HISTORY (v1):"
)

// BenchConfig configures a benchmark run. Both arms (baseline and v1) are
// always executed over the same deterministic scenario; the config describes
// the scenario, not the arms.
type BenchConfig struct {
	// Scenario is a free-form label carried into BenchResult. Empty means
	// "rnf10_context_reduction".
	Scenario string
	// NumTurns is the number of scripted conversation turns. Must be >= 1.
	NumTurns int
	// SessionID is a label only: each arm mints its own fresh temp store and
	// its own session inside it, so the two arms never share state.
	SessionID string
	// ThresholdPct is the pass/fail reduction percentage. Zero or negative
	// selects DefaultReductionThresholdPct (40, the RNF-2.3 criterion).
	ThresholdPct float64
}

// BenchResult reports the measured comparison between the two arms.
type BenchResult struct {
	Scenario   string
	TotalTurns int

	// BaselinePromptTokens is the sum over all turns of the approximated
	// prompt tokens of the naive full-history arm.
	BaselinePromptTokens int
	// V1PromptTokens is the same sum for the v1 arm (window + retrieval +
	// compaction + anchoring).
	V1PromptTokens int

	// TotalTokens equals PromptTokens: the bench measures the request
	// payload only.
	TotalTokens int
	// PromptTokens is the baseline arm's prompt-token total; the baseline is
	// the reference cost the reduction is computed against.
	PromptTokens int
	// CompletionTokens is always 0: completions are scripted, not modeled —
	// RNF-2.3 measures context (prompt) tokens.
	CompletionTokens int

	// TotalLatencyMs and AvgLatencyMs are always 0: real latency requires a
	// live model per RNF-10.1 and is out of scope for this offline pass.
	TotalLatencyMs int64
	AvgLatencyMs   float64

	// RetrievalCalls counts v1-arm turns whose built context contained a
	// retrieval injection.
	RetrievalCalls int
	// CompactionCycles counts v1-arm turns whose built context used the
	// compacted view instead of the plain sliding window.
	CompactionCycles int
	// AnchorsCreated is the number of anchors seeded into the v1 arm's
	// anchor store before the first turn.
	AnchorsCreated int
	// ModelSwitches is always 0: the v1 routing flag selects WHICH model
	// answers a turn, not what the model sees, so it cannot change context
	// size and is left off (and unmeasured) in this benchmark.
	ModelSwitches int

	// CostReductionPct is (baseline - v1) / baseline * 100.
	CostReductionPct float64
	// ThresholdPct is the threshold this run was judged against.
	ThresholdPct float64
	// MeetsThreshold reports CostReductionPct >= ThresholdPct.
	MeetsThreshold bool
}

// BenchRunner executes the benchmark. It holds no state between runs: every
// RunScenario call builds fresh temp stores for both arms.
type BenchRunner struct{}

// NewBenchRunner creates a new BenchRunner.
func NewBenchRunner() (*BenchRunner, error) {
	return &BenchRunner{}, nil
}

// RunScenario executes both arms over an identical deterministic scenario and
// returns the measured comparison. Each arm gets its own fresh temp SQLite
// store, its own tools registry, and its own session; the same turn sequence
// is replayed in both.
func (r *BenchRunner) RunScenario(ctx context.Context, config BenchConfig) (BenchResult, error) {
	if config.NumTurns <= 0 {
		return BenchResult{}, fmt.Errorf("bench: NumTurns must be >= 1, got %d", config.NumTurns)
	}
	scenario := config.Scenario
	if scenario == "" {
		scenario = "rnf10_context_reduction"
	}
	threshold := config.ThresholdPct
	if threshold <= 0 {
		threshold = DefaultReductionThresholdPct
	}

	turns := buildScenario(config.NumTurns)

	baseline, err := r.runArm(ctx, turns, naiveArmSpec)
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench: baseline arm: %w", err)
	}
	v1, err := r.runArm(ctx, turns, v1ArmSpec)
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench: v1 arm: %w", err)
	}

	baselineTokens := sumInts(baseline.perTurnTokens)
	v1Tokens := sumInts(v1.perTurnTokens)
	var reduction float64
	if baselineTokens > 0 {
		reduction = float64(baselineTokens-v1Tokens) / float64(baselineTokens) * 100
	}

	return BenchResult{
		Scenario:             scenario,
		TotalTurns:           config.NumTurns,
		BaselinePromptTokens: baselineTokens,
		V1PromptTokens:       v1Tokens,
		TotalTokens:          baselineTokens,
		PromptTokens:         baselineTokens,
		CompletionTokens:     0,
		TotalLatencyMs:       0,
		AvgLatencyMs:         0,
		RetrievalCalls:       v1.retrievalInjections,
		CompactionCycles:     v1.compactionViews,
		AnchorsCreated:       v1.anchorsSeeded,
		ModelSwitches:        0,
		CostReductionPct:     reduction,
		ThresholdPct:         threshold,
		MeetsThreshold:       reduction >= threshold,
	}, nil
}

// Summary renders a human-readable summary table for the result.
func (res BenchResult) Summary() string {
	verdict := "FAIL"
	if res.MeetsThreshold {
		verdict = "PASS"
	}
	return fmt.Sprintf(
		`RNF-10 / RNF-2.3 context-token reduction (offline, deterministic, model-free)
  scenario:      %s
  turns:         %d
  baseline:      %d prompt tokens (full history, avg %.1f/turn)
  v1:            %d prompt tokens (recent window + retrieval + compaction + anchoring, avg %.1f/turn)
  reduction:     %.1f%%  ->  %s (threshold %.1f%%)
  v1 injections: %d retrieval turns, %d compacted-view turns, %d anchors seeded
  note:          scripted completions are not modeled; latency requires a live
                 model (RNF-10.1) and is out of scope for this offline pass`,
		res.Scenario,
		res.TotalTurns,
		res.BaselinePromptTokens, float64(res.BaselinePromptTokens)/float64(max(res.TotalTurns, 1)),
		res.V1PromptTokens, float64(res.V1PromptTokens)/float64(max(res.TotalTurns, 1)),
		res.CostReductionPct, verdict, res.ThresholdPct,
		res.RetrievalCalls, res.CompactionCycles, res.AnchorsCreated,
	)
}

// armSpec describes one benchmark arm.
type armSpec struct {
	name string
	// windowTurns is the ContextAssembler maxHistoryTurns. The baseline arm
	// uses a huge value so every persisted message is always in context.
	windowTurns int
	// v1Flags writes the v1_* booleans into the session metadata, exactly
	// like a session created with the feature flags on.
	v1Flags bool
	// wireV1Deps constructs and wires real v1 dependencies (retriever,
	// compactor, anchor store) into the assembler, like the daemon does.
	wireV1Deps bool
	// seedAnchors inserts the decision anchors into the anchor store before
	// the first turn, so the anchoring injection is exercised every turn.
	seedAnchors bool
	// summaryChars is the compactor's per-message summary cap (RNF-2.5
	// tuning knob). 0 uses the compactor's default.
	summaryChars int
}

// naiveArmSpec is the RNF-2.3 baseline: "full history" every turn, no v1
// features, no v1 dependencies.
var naiveArmSpec = armSpec{
	name:        "baseline-full-history",
	windowTurns: 100000,
}

// v1ArmSpec is the production v1 configuration: the default 8-turn recent
// window and 60-char summary granularity (RNF-10-tuned), flags on, real
// dependencies wired like cli/daemon.go runServe.
var v1ArmSpec = armSpec{
	name:         "v1-window8-chars60",
	windowTurns:  8,
	v1Flags:      true,
	wireV1Deps:   true,
	seedAnchors:  true,
	summaryChars: 60,
}

// armStats collects what one arm actually did, per turn.
type armStats struct {
	perTurnTokens       []int
	perTurnMessages     []int
	retrievalInjections int
	compactionViews     int
	anchorsSeeded       int
}

// runArm replays the scripted conversation against one arm and returns its
// per-turn measurements.
//
// Per turn, in production order (agent loop + SessionManager):
//  1. persist the user message (agent loop step 1);
//  2. Build(ctx, sessionID, userMessage) — the context of the turn's first
//     LLM call — and count its prompt tokens;
//  3. persist the scripted assistant reply (completions are not modeled);
//  4. on tool-work turns, persist the scripted tool result, exactly like the
//     agent loop persists tool output for later turns;
//  5. re-index the full transcript into the retriever (SessionManager
//     .indexSession, replicated verbatim) so later turns exercise retrieval
//     the same way production does.
//
// Note on fidelity: production Build is called AFTER the user message is
// persisted, so the current user message appears once as the newest history
// entry and once as the explicit "current user message" Build appends. The
// bench replicates this exactly, and both arms pay the same duplication, so
// the comparison stays fair.
func (r *BenchRunner) runArm(ctx context.Context, turns []scenarioTurn, spec armSpec) (armStats, error) {
	stats := armStats{}

	dir, err := os.MkdirTemp("", "forge-bench-")
	if err != nil {
		return stats, fmt.Errorf("create bench temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	st, err := store.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		return stats, fmt.Errorf("open bench store: %w", err)
	}
	defer st.Close()

	// Production parity: runServe always creates the anchors table on the
	// shared session database, regardless of which flags are on.
	if err := anchor.CreateAnchorTable(ctx, st.DB()); err != nil {
		return stats, fmt.Errorf("create anchors table: %w", err)
	}

	// Base five tools only, like the default registry: tool definitions are
	// part of the prompt in both arms and add the same constant overhead to
	// every turn.
	toolsReg := tools.NewDefaultRegistry(nil, "", nil)

	metadata := map[string]any{}
	var v1 agent.V1Deps
	if spec.v1Flags {
		// Routing stays off: it selects which model answers, not what the
		// model sees, so it cannot change context size (documented in
		// BenchResult.ModelSwitches).
		metadata["v1_retrieval"] = true
		metadata["v1_compaction"] = true
		metadata["v1_anchoring"] = true
		metadata["v1_routing"] = false
	}
	if spec.wireV1Deps {
		// Same wiring as the daemon: in-memory embedding store (DSN is
		// ignored today), retriever on top, deterministic compactor, and an
		// anchor store sharing the session database handle.
		embStore, err := embedding.NewStore("")
		if err != nil {
			return stats, fmt.Errorf("create embedding store: %w", err)
		}
		defer embStore.Close()
		v1 = agent.V1Deps{
			Retriever: retrieval.NewRetriever(embStore),
			Compactor: compaction.NewCompactor(compaction.Config{
				SummaryCharsPerMessage: spec.summaryChars,
			}),
			AnchorStore: anchor.NewAnchorStoreSQL(st.DB()),
		}
	}

	session, err := st.CreateSession(ctx, metadata)
	if err != nil {
		return stats, fmt.Errorf("create session: %w", err)
	}

	assembler := agent.NewContextAssembler(toolsReg, st, spec.windowTurns)
	if spec.wireV1Deps {
		assembler.SetV1Deps(v1)
	}

	if spec.seedAnchors {
		for _, content := range anchorSeeds {
			_, err := v1.AnchorStore.Create(ctx, anchor.Anchor{
				SessionID: session.ID,
				Content:   content,
				Source:    "user",
			})
			if err != nil {
				return stats, fmt.Errorf("seed anchor: %w", err)
			}
			stats.anchorsSeeded++
		}
	}

	for i, turn := range turns {
		turnNo := i + 1

		// 1. Persist the user message first, like the agent loop does.
		if _, _, err := st.AppendMessage(ctx, &store.Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   turn.user,
		}); err != nil {
			return stats, fmt.Errorf("turn %d: append user message: %w", turnNo, err)
		}

		// 2. Build the context of the turn's first LLM call and count it.
		built, err := assembler.Build(ctx, session.ID, turn.user)
		if err != nil {
			return stats, fmt.Errorf("turn %d: build context: %w", turnNo, err)
		}
		tokens := 0
		for _, msg := range built {
			tokens += tokensFor(msg.Content)
		}
		stats.perTurnTokens = append(stats.perTurnTokens, tokens)
		stats.perTurnMessages = append(stats.perTurnMessages, len(built))

		if spec.v1Flags && spec.wireV1Deps {
			if hasPrefixedMessage(built, retrievalInjectionPrefix) {
				stats.retrievalInjections++
			}
			if hasPrefixedMessage(built, compactionInjectionPrefix) {
				stats.compactionViews++
			}
		}

		// 3. Persist the scripted assistant reply.
		if _, _, err := st.AppendMessage(ctx, &store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   turn.assistant,
		}); err != nil {
			return stats, fmt.Errorf("turn %d: append assistant message: %w", turnNo, err)
		}

		// 4. Persist the scripted tool result on tool-work turns.
		if turn.toolOut != "" {
			if _, _, err := st.AppendMessage(ctx, &store.Message{
				SessionID: session.ID,
				Role:      "tool",
				Name:      turn.toolName,
				Content:   turn.toolOut,
			}); err != nil {
				return stats, fmt.Errorf("turn %d: append tool result: %w", turnNo, err)
			}
		}

		// 5. Replicate SessionManager.indexSession: rebuild the retrieval
		// index from the full transcript after each turn. Production treats
		// this as best-effort; the bench fails loudly instead, because a
		// silently degraded index would corrupt the measurement.
		if spec.v1Flags && v1.Retriever != nil {
			if err := indexSessionForBench(ctx, st, v1.Retriever, session.ID); err != nil {
				return stats, fmt.Errorf("turn %d: %w", turnNo, err)
			}
		}
	}

	return stats, nil
}

// indexSessionForBench replicates daemon.SessionManager.indexSession: fetch
// the full transcript and re-index it into the retriever.
func indexSessionForBench(ctx context.Context, st *store.Store, retriever *retrieval.Retriever, sessionID string) error {
	msgs, err := st.GetMessagesSince(ctx, sessionID, 0)
	if err != nil {
		return fmt.Errorf("fetch transcript for indexing: %w", err)
	}
	index := make([]retrieval.Message, 0, len(msgs))
	for _, msg := range msgs {
		index = append(index, retrieval.Message{
			ID:      msg.ID,
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	if err := retriever.Index(index); err != nil {
		return fmt.Errorf("index transcript: %w", err)
	}
	return nil
}

// tokensFor approximates the token count of a message's text content: one
// token per ~4 characters plus a fixed per-message overhead (role framing).
// This mirrors the rough len/4 heuristic the compactor itself uses.
//
// The SAME formula is applied to EVERY message returned by Build in BOTH
// arms — system prompt, tool definitions, injections, history, and the
// current user message — so prompt-prefix costs are captured naturally and
// identically. Consistency across arms is what makes the comparison valid,
// not the absolute accuracy of the estimate.
func tokensFor(text string) int {
	return (len(text)+3)/4 + 4 // ceil(len/4) + 4
}

// hasPrefixedMessage reports whether any built message starts with prefix.
func hasPrefixedMessage(msgs []llm.Message, prefix string) bool {
	for _, msg := range msgs {
		if strings.HasPrefix(msg.Content, prefix) {
			return true
		}
	}
	return false
}

func sumInts(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}
