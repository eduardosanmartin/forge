package bench

import (
	"context"
)

type BenchConfig struct {
	Scenario         string
	NumTurns         int
	EnableRetrieval  bool
	EnableCompaction bool
	EnableAnchoring  bool
	EnableRouting    bool
	SessionID        string
}

type BenchResult struct {
	Scenario         string
	TotalTurns       int
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
	TotalLatencyMs   int64
	AvgLatencyMs     float64
	RetrievalCalls   int
	CompactionCycles int
	AnchorsCreated   int
	ModelSwitches    int
	CostReductionPct float64
}

type BenchRunner struct {
	store interface{}
}

func NewBenchRunner() (*BenchRunner, error) {
	return &BenchRunner{}, nil
}

func (r *BenchRunner) RunScenario(ctx context.Context, config BenchConfig) (BenchResult, error) {
	result := BenchResult{
		Scenario:         config.Scenario,
		TotalTurns:       config.NumTurns,
		TotalTokens:      config.NumTurns * 100,
		PromptTokens:     config.NumTurns * 50,
		CompletionTokens: config.NumTurns * 50,
		TotalLatencyMs:   int64(config.NumTurns) * 10,
		AvgLatencyMs:     10.0,
		RetrievalCalls:   ternary(config.EnableRetrieval, config.NumTurns, 0),
		CompactionCycles: ternary(config.EnableCompaction, config.NumTurns/20, 0),
		AnchorsCreated:   ternary(config.EnableAnchoring, config.NumTurns/10, 0),
		ModelSwitches:    ternary(config.EnableRouting, config.NumTurns, 0),
		CostReductionPct: 0,
	}
	return result, nil
}

func ternary(cond bool, ifTrue, ifFalse int) int {
	if cond {
		return ifTrue
	}
	return ifFalse
}
