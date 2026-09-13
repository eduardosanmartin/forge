package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// TestSpawnChildren_ParallelExecutesMultiple verifies that SpawnChildren with
// multiple specs executes more than one child concurrently (RF-1.2). It tracks
// observed peak concurrency instead of wall time so the assertion is
// deterministic under load (a timing bound flaked on busy CI runs).
func TestSpawnChildren_ParallelExecutesMultiple(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Agent.MaxParallelChildren = 2
	storeImpl := newBranchMockStore()
	// Provider that sleeps 40ms to make overlap observable.
	var concurrent int32
	var maxSeen int32
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			m := atomic.LoadInt32(&maxSeen)
			if cur > m && atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
				break
			}
			if cur <= m {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}},
			Usage:   &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		}, nil
	})
	llmReg := &mockLLMRegistry{provider: provider}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	if agent.MaxParallelChildren() != 2 {
		t.Fatalf("max parallel want 2 got %d", agent.MaxParallelChildren())
	}
	specs := []ChildSpec{{Task: "task A"}, {Task: "task B"}, {Task: "task C"}}
	results, errs := agent.SpawnChildren(ctx, "parent-1", specs)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("child %d err %v", i, err)
		}
		if !results[i].Success {
			t.Fatalf("child %d not success %q", i, results[i].Error)
		}
	}
	// With 3 specs and pool 2, at least two children must overlap (peak >= 2).
	// TestSpawnChildren_RespectsMax pins the upper bound (never above the pool).
	if atomic.LoadInt32(&maxSeen) < 2 {
		t.Fatalf("children did not execute concurrently: peak concurrency %d want >= 2", atomic.LoadInt32(&maxSeen))
	}
}

// TestSpawnChildren_RespectsMax verifies the pool size is honored.
func TestSpawnChildren_RespectsMax(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Agent.MaxParallelChildren = 2
	storeImpl := newBranchMockStore()
	var concurrent int32
	var maxSeen int32
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		// track max
		for {
			m := atomic.LoadInt32(&maxSeen)
			if cur > m && atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
				break
			}
			if cur <= m {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "ok"}}},
			Usage:   &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		}, nil
	})
	llmReg := &mockLLMRegistry{provider: provider}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	specs := []ChildSpec{{Task: "a"}, {Task: "b"}, {Task: "c"}, {Task: "d"}}
	_, errs := agent.SpawnChildren(ctx, "parent-1", specs)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("child %d err %v", i, err)
		}
	}
	max := atomic.LoadInt32(&maxSeen)
	if max > 2 {
		t.Fatalf("max concurrent %d exceeds limit 2", max)
	}
	if max < 2 {
		t.Fatalf("max concurrent %d want 2 (should saturate pool)", max)
	}
}

// TestSpawnChildren_ConfigClamping ensures 2-4 bounds (default 2).
func TestSpawnChildren_ConfigClamping(t *testing.T) {
	cfg := config.Defaults()
	cfg.Agent.MaxParallelChildren = 99
	storeImpl := newBranchMockStore()
	llmReg := newMockLLMRegistry(&llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "x"}}},
	})
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	if agent.MaxParallelChildren() != 4 {
		t.Fatalf("clamp high want 4 got %d", agent.MaxParallelChildren())
	}
	cfg2 := config.Defaults()
	cfg2.Agent.MaxParallelChildren = 0
	agent2 := NewAgent(cfg2, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	if agent2.MaxParallelChildren() != 2 {
		t.Fatalf("default want 2 got %d", agent2.MaxParallelChildren())
	}
	cfg3 := config.Defaults()
	cfg3.Agent.MaxParallelChildren = 1
	agent3 := NewAgent(cfg3, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	if agent3.MaxParallelChildren() != 2 {
		t.Fatalf("low clamp want 2 got %d", agent3.MaxParallelChildren())
	}
}

// TestParallelToolCalls_DispatchViaLoop verifies the loop parallel path:
// a single turn whose LLM returns two spawn_subagent calls executes them in
// parallel (rather than sequentially).
func TestParallelToolCalls_DispatchViaLoop(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Agent.MaxParallelChildren = 2
	storeImpl := newBranchMockStore()
	_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "user", Content: "seed"})
	var maxConc int32
	var cur int32
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		// Detect child turns by last user = task A/B
		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}
		if lastUser == "task A" || lastUser == "task B" {
			c := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&maxConc)
				if c > m && atomic.CompareAndSwapInt32(&maxConc, m, c) {
					break
				}
				if c <= m {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "child:" + lastUser}}}}, nil
		}
		// Parent first call returns two spawn tools
		if lastUser == "do both" {
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{
					Role:    "assistant",
					Content: "spawn two",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task A"}`}},
						{ID: "c2", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task B"}`}},
					},
				}}},
			}, nil
		}
		return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "parent done"}}}}, nil
	})
	llmReg := &mockLLMRegistry{provider: provider}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.New(permsEng, "", newTestLogger())
	toolsReg.Register(&stubFsReadTool{})
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	spawnTool := tools.NewSpawnSubagentTool()
	spawnTool.SetSpawner(func(toolCtx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (tools.Result, error) {
		parentID := tools.SessionIDFromContext(toolCtx)
		child, err := agent.SpawnChild(toolCtx, parentID, ChildSpec{Task: task})
		if err != nil {
			return tools.Result{Content: "ERROR: " + err.Error()}, nil
		}
		return tools.Result{Content: "SUMMARY " + child.Summary}, nil
	})
	toolsReg.Register(spawnTool)
	start := time.Now()
	result, err := agent.ExecuteTurn(ctx, "parent-1", "do both")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	elapsed := time.Since(start)
	// Two children sleep 30ms each; sequential =60ms, parallel ~30ms.
	if elapsed > 80*time.Millisecond {
		t.Fatalf("parallel loop dispatch too slow: %s want <80ms, messages %+v", elapsed, result.Messages)
	}
	if atomic.LoadInt32(&maxConc) < 2 {
		t.Fatalf("loop dispatch did not run children concurrently, maxConc=%d", atomic.LoadInt32(&maxConc))
	}
}
