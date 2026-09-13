// Package agent implements bounded parallel scheduling for subagent child turns (RF-1.2).
//
// Parallelism limits (documented at the type and enforced here):
//   - Worker pool size via agent.max_parallel_children: 2-4, default 2 (config).
//   - Each child runs in a distinct branched session (BranchSession with its own
//     session ID), so children never share a SQLite write transaction.
//   - The underlying store is a single SQLite connection (store.Open MaxOpenConns=1,
//     WAL + busy_timeout=5000). Writes are serialized by the DB; parallelism is
//     effective for LLM calls (network-bound) while DB appends serialize. With
//     distinct session IDs the logical contention is minimal (different rows).
//   - Parent transcript appends (tool results) are serialized after child joins
//     to avoid racing MAX(seq) on the same parent session.
//
// If SQLite blocks, the driver retries via busy_timeout; no additional locking
// is introduced beyond the bounded semaphore. Non-subagent tool calls are not
// parallelized (they may touch the same files/session).
package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// MaxParallelChildren returns the configured parallelism for this agent.
func (a *Agent) MaxParallelChildren() int {
	if a.maxParallelChildren <= 0 {
		return 2
	}
	return a.maxParallelChildren
}

// boundedMap is the single bounded worker pool shared by every RF-1.2 parallel
// path (SpawnChildren and the loop's spawn_subagent dispatch). It runs jobs in
// parallel, bounded by limit, blocking until all complete. Output order matches
// input order. A limit below 1 falls back to 2.
func boundedMap[T any](limit int, jobs []func() T) []T {
	n := len(jobs)
	out := make([]T, n)
	if n == 0 {
		return out
	}
	if limit < 1 {
		limit = 2
	}
	if limit > n {
		limit = n
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, j func() T) {
			defer wg.Done()
			defer func() { <-sem }()
			out[idx] = j()
		}(i, job)
	}
	wg.Wait()
	return out
}

// SpawnChildren runs multiple ChildSpec turns in parallel bounded by
// maxParallelChildren. Each child is isolated in its own branched session
// (SpawnChild's BranchSession). Order of results matches input order.
// A zero or negative pool size falls back to the agent's configured limit.
func (a *Agent) SpawnChildren(ctx context.Context, parentSessionID string, specs []ChildSpec) ([]ChildResult, []error) {
	jobs := make([]func() childOutcome, len(specs))
	for i, spec := range specs {
		s := spec
		jobs[i] = func() childOutcome {
			res, err := a.SpawnChild(ctx, parentSessionID, s)
			return childOutcome{result: res, err: err}
		}
	}
	outs := boundedMap(a.MaxParallelChildren(), jobs)
	results := make([]ChildResult, len(outs))
	errs := make([]error, len(outs))
	for i, out := range outs {
		results[i] = out.result
		errs[i] = out.err
	}
	return results, errs
}

// childOutcome pairs a SpawnChild result with its error for collection.
type childOutcome struct {
	result ChildResult
	err    error
}

// parallelToolOutcome pairs a dispatched tool call with its execution result so
// the caller can append tool-result messages paired by ToolCallID after the
// join, serially, on the parent transcript.
type parallelToolOutcome struct {
	call   llm.ToolCall
	result tools.Result
}

// executeToolCallsParallel dispatches spawn_subagent tool calls through the
// bounded worker pool (the shared RF-1.2 scheduler). Execution goes through
// toolsReg.Execute so permission checks, fencing, and redaction apply per
// call. Outcomes are returned in input order; the caller owns appending tool
// results to the parent session (serially, after this call returns).
func (a *Agent) executeToolCallsParallel(ctx context.Context, sessionID string, calls []llm.ToolCall) []parallelToolOutcome {
	jobs := make([]func() parallelToolOutcome, len(calls))
	for i, call := range calls {
		c := call
		jobs[i] = func() parallelToolOutcome {
			var args map[string]any
			if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
				args = map[string]any{"_error": "invalid arguments: " + err.Error()}
			}
			toolCtx := tools.WithSessionID(ctx, sessionID)
			tr, err := a.toolsReg.Execute(toolCtx, c.Function.Name, args)
			if err != nil {
				tr = tools.Result{Content: "ERROR: " + err.Error()}
			}
			return parallelToolOutcome{call: c, result: tr}
		}
	}
	return boundedMap(a.MaxParallelChildren(), jobs)
}
