package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestSpawnViaTool_ParentReceivesChildSummary(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newBranchMockStore()
	// Seed parent with one message so branch has history.
	_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "user", Content: "initial"})
	_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "assistant", Content: "welcome"})

	// Shared mock LLM that distinguishes parent vs child by task content.
	// Call sequence for sequential parent→child→parent: 1=parent spawn, 2=child answer, 3=parent final.
	callNum := 0
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		callNum++
		// Detect child turn by the presence of the bounded task as the last user message.
		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}
		if strings.Contains(lastUser, "child work") {
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "child final answer for child work"}}},
				Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
			}, nil
		}
		if callNum == 1 {
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{
					Role:    "assistant",
					Content: "spawning child",
					ToolCalls: []llm.ToolCall{{
						ID:   "call-spawn",
						Type: "function",
						Function: llm.ToolCallFunction{
							Name:      "spawn_subagent",
							Arguments: `{"task":"child work","max_iterations":5,"token_budget":200}`,
						},
					}},
				}}},
				Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			}, nil
		}
		// Parent final after tool result.
		return llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "parent final after child"}}},
			Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	})
	llmReg := &mockLLMRegistry{provider: provider}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.New(permsEng, "", newTestLogger())
	for _, tl := range []tools.Tool{
		&stubFsReadTool{},
	} {
		toolsReg.Register(tl)
	}
	// Wire spawn_subagent manually like daemon does.
	spawnTool := tools.NewSpawnSubagentTool()
	// Need agent instance to call SpawnChild; create agent first with this registry.
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	var capturedChildID string
	spawnTool.SetSpawner(func(toolCtx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (tools.Result, error) {
		parentID := tools.SessionIDFromContext(toolCtx)
		spec := ChildSpec{Task: task, MaxIterations: maxIter, TokenBudget: tokenBudget, FileBudget: fileBudget}
		child, err := agent.SpawnChild(toolCtx, parentID, spec)
		if err != nil {
			return tools.Result{Content: "ERROR: " + err.Error()}, nil
		}
		capturedChildID = child.ChildSessionID
		status := "success"
		if !child.Success {
			status = "failed"
		}
		content := "subagent " + child.ChildSessionID + " [" + status + "] SUMMARY:\n" + child.Summary
		return tools.Result{Content: content, Metadata: map[string]any{"subagent_session_id": child.ChildSessionID, "success": child.Success}}, nil
	})
	toolsReg.Register(spawnTool)

	result, err := agent.ExecuteTurn(ctx, "parent-1", "please delegate")
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if result.Halted {
		t.Fatalf("halted: %v", result.Error)
	}
	// Parent transcript must contain tool result with child summary.
	foundTool := false
	foundFinal := false
	for _, m := range result.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "child final answer for child work") {
			foundTool = true
		}
		if m.Role == "assistant" && m.Content == "parent final after child" {
			foundFinal = true
		}
	}
	if !foundTool {
		t.Fatalf("parent transcript missing child summary in tool result; messages: %+v", result.Messages)
	}
	if !foundFinal {
		t.Fatal("parent final assistant missing")
	}
	// Child transcript lives in branched session.
	childSessionID := capturedChildID
	if childSessionID == "" {
		t.Fatal("could not capture child session id")
	}
	childMsgs, _ := storeImpl.GetMessagesSince(ctx, childSessionID, 0)
	hasChildUser := false
	hasChildAssistant := false
	for _, m := range childMsgs {
		if m.Role == "user" && m.Content == "child work" {
			hasChildUser = true
		}
		if m.Role == "assistant" && strings.Contains(m.Content, "child final answer") {
			hasChildAssistant = true
		}
	}
	if !hasChildUser || !hasChildAssistant {
		t.Fatalf("child transcript incomplete: hasUser=%v hasAssistant=%v msgs=%+v", hasChildUser, hasChildAssistant, childMsgs)
	}
	// Permission inheritance: child's tool registry same instance, spawn tool also inherits fence (verified by tool result being fenced).
	for _, m := range result.Messages {
		if m.Role == "tool" && m.Name == "spawn_subagent" {
			if !strings.Contains(m.Content, "<<TOOL_RESULT:spawn_subagent>>") {
				t.Fatalf("spawn result must be fenced, got %q", m.Content)
			}
		}
	}
}

func TestSpawnViaTool_SequentialMultiChildInOneTurn(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newBranchMockStore()
	_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "user", Content: "seed"})

	callNum := 0
	provider := newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
		callNum++
		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}
		if lastUser == "task A" {
			return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "result A"}}}}, nil
		}
		if lastUser == "task B" {
			return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "result B"}}}}, nil
		}
		if callNum == 1 {
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{
					Role:    "assistant",
					Content: "spawn two",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task A"}`}},
						{ID: "c2", Type: "function", Function: llm.ToolCallFunction{Name: "spawn_subagent", Arguments: `{"task":"task B"}`}},
					},
				}}},
				Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}, nil
		}
		return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "parent done"}}}}, nil
	})
	llmReg := &mockLLMRegistry{provider: provider}
	permsEng2 := newTestPermsEngine(t)
	toolsReg2 := tools.New(permsEng2, "", newTestLogger())
	toolsReg2.Register(&stubFsReadTool{})
	agent2 := NewAgent(cfg, storeImpl, llmReg, toolsReg2, permsEng2, newTestLogger())
	spawnTool2 := tools.NewSpawnSubagentTool()
	spawnTool2.SetSpawner(func(toolCtx context.Context, task string, maxIter int, tokenBudget int, fileBudget string) (tools.Result, error) {
		parentID := tools.SessionIDFromContext(toolCtx)
		child, err := agent2.SpawnChild(toolCtx, parentID, ChildSpec{Task: task})
		if err != nil {
			return tools.Result{Content: "ERROR: " + err.Error()}, nil
		}
		return tools.Result{Content: "SUMMARY " + child.Summary}, nil
	})
	toolsReg2.Register(spawnTool2)

	result, err := agent2.ExecuteTurn(ctx, "parent-1", "do both")
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	toolCount := 0
	hasA, hasB := false, false
	for _, m := range result.Messages {
		if m.Role == "tool" {
			toolCount++
			if strings.Contains(m.Content, "result A") {
				hasA = true
			}
			if strings.Contains(m.Content, "result B") {
				hasB = true
			}
		}
	}
	if toolCount != 2 {
		t.Fatalf("want 2 tool results sequential, got %d", toolCount)
	}
	if !hasA || !hasB {
		t.Fatalf("missing summaries hasA=%v hasB=%v", hasA, hasB)
	}
	// Sequential execution note documented in subagent.go; true parallelism
	// would require concurrent LLM/provider + SQLite WAL — follow-up.
}

type stubFsReadTool struct{}

func (t *stubFsReadTool) Name() string { return "fs_read" }
func (t *stubFsReadTool) Description() string { return "stub" }
func (t *stubFsReadTool) JSONSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}
}
func (t *stubFsReadTool) Execute(ctx context.Context, req perms.Request) (tools.Result, error) {
	return tools.Result{Content: "stub"}, nil
}
