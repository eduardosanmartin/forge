package bench

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestBenchTokenReduction is the RNF-10 / RNF-2.3 entry point: it runs both
// arms (naive full history vs v1) over the deterministic scenario and reports
// the measured context-token reduction.
//
// The number of turns comes from FORGE_BENCH_TURNS (default 40, satisfying
// RNF-2.3's "more than 20 turns"), so scripts/run-bench.ps1 can parametrize
// the run without a dedicated binary.
func TestBenchTokenReduction(t *testing.T) {
	turns := 40
	if v := os.Getenv("FORGE_BENCH_TURNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("FORGE_BENCH_TURNS must be a positive integer, got %q", v)
		}
		turns = n
	}

	runner, err := NewBenchRunner()
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}
	result, err := runner.RunScenario(context.Background(), BenchConfig{
		Scenario:  "rnf10_context_reduction",
		NumTurns:  turns,
		SessionID: "bench-rnf10",
	})
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}

	t.Log(result.Summary())

	// Arithmetic consistency of the reported numbers.
	if result.PromptTokens != result.BaselinePromptTokens {
		t.Errorf("PromptTokens = %d, want BaselinePromptTokens %d (baseline is the reference cost)",
			result.PromptTokens, result.BaselinePromptTokens)
	}
	if result.TotalTokens != result.PromptTokens+result.CompletionTokens {
		t.Errorf("TotalTokens = %d, want PromptTokens+CompletionTokens = %d",
			result.TotalTokens, result.PromptTokens+result.CompletionTokens)
	}
	if result.BaselinePromptTokens <= 0 || result.V1PromptTokens <= 0 {
		t.Errorf("prompt token totals must be positive, got baseline=%d v1=%d",
			result.BaselinePromptTokens, result.V1PromptTokens)
	}
	wantReduction := float64(result.BaselinePromptTokens-result.V1PromptTokens) /
		float64(result.BaselinePromptTokens) * 100
	if diff := result.CostReductionPct - wantReduction; diff < -1e-9 || diff > 1e-9 {
		t.Errorf("CostReductionPct = %f, want %f", result.CostReductionPct, wantReduction)
	}
	if result.MeetsThreshold != (result.CostReductionPct >= result.ThresholdPct) {
		t.Errorf("MeetsThreshold = %v, inconsistent with reduction %f vs threshold %f",
			result.MeetsThreshold, result.CostReductionPct, result.ThresholdPct)
	}

	// v1-arm mechanics: retrieval re-indexing feeds injections from the
	// second turn on; compaction kicks in once the transcript crosses the
	// assembler's threshold.
	if turns >= 2 && result.RetrievalCalls == 0 {
		t.Errorf("RetrievalCalls = 0, want > 0 for a %d-turn v1 session", turns)
	}
	if turns >= 25 && result.CompactionCycles == 0 {
		t.Errorf("CompactionCycles = 0, want > 0 for a %d-turn v1 session", turns)
	}
	if result.AnchorsCreated != len(anchorSeeds) {
		t.Errorf("AnchorsCreated = %d, want %d (seeded anchors)", result.AnchorsCreated, len(anchorSeeds))
	}
	if result.ModelSwitches != 0 {
		t.Errorf("ModelSwitches = %d, want 0 (routing does not change context size and is off)", result.ModelSwitches)
	}

	// Machine-readable line for scripts/run-bench.ps1 -Json.
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	t.Logf("BENCH_JSON %s", b)
}

// TestBenchBaselinePromptTokensGrow pins the baseline arm's defining
// property: with full history, per-turn prompt tokens grow (linearly in the
// per-turn payload, quadratically in the cumulative sum) as the session gets
// longer, and no v1 machinery is active.
func TestBenchBaselinePromptTokensGrow(t *testing.T) {
	ctx := context.Background()
	runner, err := NewBenchRunner()
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}
	turns := buildScenario(30)

	stats, err := runner.runArm(ctx, turns, naiveArmSpec)
	if err != nil {
		t.Fatalf("runArm(naive): %v", err)
	}

	if stats.retrievalInjections != 0 || stats.compactionViews != 0 || stats.anchorsSeeded != 0 {
		t.Errorf("baseline arm must have no v1 activity, got retrieval=%d compaction=%d anchors=%d",
			stats.retrievalInjections, stats.compactionViews, stats.anchorsSeeded)
	}

	// Full history: every turn's context strictly contains the previous
	// turn's plus new content, so prompt tokens strictly increase.
	for i := 1; i < len(stats.perTurnTokens); i++ {
		if stats.perTurnTokens[i] <= stats.perTurnTokens[i-1] {
			t.Fatalf("baseline per-turn tokens not strictly increasing at turn %d: %d <= %d",
				i+1, stats.perTurnTokens[i], stats.perTurnTokens[i-1])
		}
	}

	// Growth over 20 additional turns must be substantial (linear per-turn
	// growth => roughly 3x between turn 10 and turn 30).
	if got := stats.perTurnTokens[29]; got <= 2*stats.perTurnTokens[9] {
		t.Errorf("baseline turn-30 prompt tokens = %d, want more than 2x turn-10 (%d): context is not growing",
			got, stats.perTurnTokens[9])
	}
}

// TestBenchV1PromptTokensBounded pins the v1 arm's defining property: once
// the recent window saturates, per-turn prompt tokens stay in a bounded band
// instead of growing with the session length.
func TestBenchV1PromptTokensBounded(t *testing.T) {
	ctx := context.Background()
	runner, err := NewBenchRunner()
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}
	turns := buildScenario(40)

	stats, err := runner.runArm(ctx, turns, v1ArmSpec)
	if err != nil {
		t.Fatalf("runArm(v1): %v", err)
	}

	if stats.retrievalInjections < 38 {
		t.Errorf("retrieval injections = %d, want >= 38 (every turn after the first re-index)", stats.retrievalInjections)
	}
	if stats.compactionViews < 15 {
		t.Errorf("compacted-view turns = %d, want >= 15 (transcript crosses the threshold around turn 19)", stats.compactionViews)
	}
	if stats.anchorsSeeded != len(anchorSeeds) {
		t.Errorf("anchors seeded = %d, want %d", stats.anchorsSeeded, len(anchorSeeds))
	}

	// Bounded band from turn 20 on: the window is saturated and the only
	// growth left is the compacted summary, which is far smaller than the
	// verbatim history it replaces.
	minTok, maxTok := stats.perTurnTokens[19], stats.perTurnTokens[19]
	for _, tok := range stats.perTurnTokens[19:] {
		if tok < minTok {
			minTok = tok
		}
		if tok > maxTok {
			maxTok = tok
		}
	}
	if maxTok > 2*minTok {
		t.Errorf("v1 per-turn tokens not bounded from turn 20 on: min=%d max=%d (max > 2x min)",
			minTok, maxTok)
	}
}

// TestBenchTinyScenarioMessageCounts asserts the exact message structure of a
// tiny 3-turn scenario, computed by hand from Build's layout:
//
//	system(1) + tool defs(5) + history(2t-1, including the just-persisted
//	user message) + current user(1), plus one anchored-facts message and one
//	retrieval message in the v1 arm when active.
//
// This pins the token arithmetic against structural expectations instead of
// trusting the runner to sum something reasonable.
func TestBenchTinyScenarioMessageCounts(t *testing.T) {
	ctx := context.Background()
	runner, err := NewBenchRunner()
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}
	turns := buildScenario(3)

	naive, err := runner.runArm(ctx, turns, naiveArmSpec)
	if err != nil {
		t.Fatalf("runArm(naive): %v", err)
	}
	wantNaive := []int{8, 10, 12} // 6 fixed + (2t-1) history + 1 current user
	if !reflect.DeepEqual(naive.perTurnMessages, wantNaive) {
		t.Errorf("naive per-turn messages = %v, want %v", naive.perTurnMessages, wantNaive)
	}

	v1, err := runner.runArm(ctx, turns, v1ArmSpec)
	if err != nil {
		t.Fatalf("runArm(v1): %v", err)
	}
	wantV1 := []int{9, 12, 14} // naive + 1 anchored-facts message; +1 retrieval from turn 2
	if !reflect.DeepEqual(v1.perTurnMessages, wantV1) {
		t.Errorf("v1 per-turn messages = %v, want %v", v1.perTurnMessages, wantV1)
	}

	// On a tiny session the injections cost more than the window saves, so
	// the honest reduction is negative: v1 is not a win for short sessions.
	naiveTotal := sumInts(naive.perTurnTokens)
	v1Total := sumInts(v1.perTurnTokens)
	if v1Total <= naiveTotal {
		t.Errorf("v1 tokens = %d, want > naive %d on a 3-turn session (injection overhead dominates)",
			v1Total, naiveTotal)
	}

	result, err := runner.RunScenario(context.Background(), BenchConfig{NumTurns: 3})
	if err != nil {
		t.Fatalf("RunScenario(3 turns): %v", err)
	}
	if result.MeetsThreshold {
		t.Errorf("MeetsThreshold = true on a 3-turn session, want false")
	}
	if result.CostReductionPct >= 0 {
		t.Errorf("CostReductionPct = %f, want < 0 on a 3-turn session", result.CostReductionPct)
	}
}

// TestBenchFlagsAndDepsNilSafety pins the nil-safety invariants of the v1
// wiring: flags without dependencies and dependencies without flags must both
// produce byte-identical context sizing to the plain baseline, because every
// v1 injection is gated on flag AND dependency.
func TestBenchFlagsAndDepsNilSafety(t *testing.T) {
	ctx := context.Background()
	runner, err := NewBenchRunner()
	if err != nil {
		t.Fatalf("NewBenchRunner: %v", err)
	}
	turns := buildScenario(15)

	naive, err := runner.runArm(ctx, turns, naiveArmSpec)
	if err != nil {
		t.Fatalf("runArm(naive): %v", err)
	}

	for _, spec := range []armSpec{
		{name: "flags-on-no-deps", windowTurns: 100000, v1Flags: true},
		{name: "deps-no-flags", windowTurns: 100000, wireV1Deps: true},
	} {
		stats, err := runner.runArm(ctx, turns, spec)
		if err != nil {
			t.Fatalf("runArm(%s): %v", spec.name, err)
		}
		if !reflect.DeepEqual(stats.perTurnTokens, naive.perTurnTokens) {
			t.Errorf("%s: per-turn tokens diverge from baseline:\n got %v\nwant %v",
				spec.name, stats.perTurnTokens, naive.perTurnTokens)
		}
		if !reflect.DeepEqual(stats.perTurnMessages, naive.perTurnMessages) {
			t.Errorf("%s: per-turn message counts diverge from baseline:\n got %v\nwant %v",
				spec.name, stats.perTurnMessages, naive.perTurnMessages)
		}
	}
}

// TestScenarioMessageLengths keeps the scripted conversation within the
// realistic developer-session profile the benchmark documents: user and
// assistant messages between 60 and 150 words, tool results multi-line.
func TestScenarioMessageLengths(t *testing.T) {
	turns := buildScenario(40)
	for i, turn := range turns {
		turnNo := i + 1
		if got := countWords(turn.user); got < 60 || got > 150 {
			t.Errorf("turn %d: user message has %d words, want 60..150", turnNo, got)
		}
		if got := countWords(turn.assistant); got < 60 || got > 150 {
			t.Errorf("turn %d: assistant message has %d words, want 60..150", turnNo, got)
		}
		if turn.toolOut != "" {
			if lines := strings.Count(turn.toolOut, "\n") + 1; lines < 8 {
				t.Errorf("turn %d: tool result has %d lines, want >= 8", turnNo, lines)
			}
		}
	}
}

// TestBuildScenarioDeterministic guards the determinism contract: identical
// turn counts must produce byte-identical scenarios.
func TestBuildScenarioDeterministic(t *testing.T) {
	a := buildScenario(40)
	b := buildScenario(40)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("buildScenario(40) produced different transcripts across calls")
	}
}

func countWords(s string) int {
	return len(strings.Fields(s))
}
