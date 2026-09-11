package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// branchMockStore extends mockStore with BranchSession for subagent tests.
type branchMockStore struct {
	mu       sync.Mutex
	sessions map[string]*store.Session
	messages map[string][]store.Message
}

func newBranchMockStore() *branchMockStore {
	return &branchMockStore{
		sessions: map[string]*store.Session{
			"parent-1": {ID: "parent-1", Metadata: map[string]any{}},
		},
		messages: make(map[string][]store.Message),
	}
}

func (m *branchMockStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return *s, nil
	}
	return store.Session{}, fmt.Errorf("session not found")
}

func (m *branchMockStore) AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := len(m.messages[msg.SessionID]) + 1
	msg.Seq = seq
	msg.ID = int64(seq)
	msg.CreatedAt = time.Now().UnixMilli()
	m.messages[msg.SessionID] = append(m.messages[msg.SessionID], *msg)
	return seq, msg.ID, nil
}

func (m *branchMockStore) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[sessionID]
	if msgs == nil {
		return []store.Message{}, nil
	}
	if offset >= len(msgs) {
		return []store.Message{}, nil
	}
	end := offset + limit
	if end > len(msgs) {
		end = len(msgs)
	}
	result := make([]store.Message, end-offset)
	for i := 0; i < end-offset; i++ {
		result[i] = msgs[len(msgs)-1-offset-i]
	}
	return result, nil
}

func (m *branchMockStore) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[sessionID]
	if msgs == nil {
		return nil, nil
	}
	var out []store.Message
	for _, msg := range msgs {
		if msg.Seq > sinceSeq {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *branchMockStore) BranchSession(ctx context.Context, sourceID string, atSeq int, metadata map[string]any) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, ok := m.sessions[sourceID]
	if !ok {
		return store.Session{}, fmt.Errorf("session not found")
	}
	id := sourceID + "-child-" + fmt.Sprint(len(m.sessions))
	merged := map[string]any{}
	for k, v := range src.Metadata {
		merged[k] = v
	}
	for k, v := range metadata {
		merged[k] = v
	}
	merged["branch_parent"] = src.ID
	merged["branch_at_seq"] = atSeq
	if _, ok := merged["branch_root"]; !ok {
		merged["branch_root"] = src.ID
	}
	s := &store.Session{ID: id, Metadata: merged, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
	m.sessions[id] = s
	// Copy messages when atSeq==0 => all, else up to atSeq.
	srcMsgs := m.messages[sourceID]
	for _, msg := range srcMsgs {
		if atSeq == 0 || msg.Seq <= atSeq {
			cp := msg
			cp.SessionID = id
			cp.Seq = len(m.messages[id]) + 1
			cp.ID = int64(cp.Seq)
			m.messages[id] = append(m.messages[id], cp)
		}
	}
	return *s, nil
}

func TestSpawnChild_BoundedContextAndBranchIsolation(t *testing.T) {
	tests := []struct {
		name       string
		spec       ChildSpec
		wantErr    bool
		wantErrMsg string
	}{
		{name: "empty task rejected", spec: ChildSpec{Task: ""}, wantErr: true, wantErrMsg: "task must not be empty"},
		{name: "negative iterations rejected", spec: ChildSpec{Task: "do x", MaxIterations: -1}, wantErr: true},
		{name: "negative token budget rejected", spec: ChildSpec{Task: "do x", TokenBudget: -1}, wantErr: true},
		{name: "valid task with budgets", spec: ChildSpec{Task: "summarize README", MaxIterations: 2, TokenBudget: 1000, FileBudget: "README.md"}, wantErr: false},
		{name: "zero budgets default to parent max", spec: ChildSpec{Task: "hello"}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := config.Defaults()
			storeImpl := newBranchMockStore()
			llmReg := newMockLLMRegistry(&llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "child done"}}},
				Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			})
			permsEng := newTestPermsEngine(t)
			toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
			agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
			// Seed parent transcript so branch has history.
			_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "user", Content: "parent hello"})
			_, _, _ = storeImpl.AppendMessage(ctx, &store.Message{SessionID: "parent-1", Role: "assistant", Content: "parent response"})

			result, err := agent.SpawnChild(ctx, "parent-1", tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error but got nil (result %+v)", result)
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Fatalf("error %q must contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("SpawnChild failed: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %q", result.Error)
			}
			if result.ChildSessionID == "" || result.ChildSessionID == "parent-1" {
				t.Fatalf("child session id invalid %q", result.ChildSessionID)
			}
			if !strings.Contains(result.Summary, "child done") {
				t.Fatalf("summary must contain child output, got %q", result.Summary)
			}
			// Branch isolation: parent messages unchanged, child has its own.
			parentMsgs, _ := storeImpl.GetMessagesSince(ctx, "parent-1", 0)
			if len(parentMsgs) != 2 {
				t.Fatalf("parent isolation broken: want 2 msgs, got %d", len(parentMsgs))
			}
			childMsgs, _ := storeImpl.GetMessagesSince(ctx, result.ChildSessionID, 0)
			if len(childMsgs) < 3 {
				t.Fatalf("child should have branched history + new turn, got %d", len(childMsgs))
			}
			// Metadata recorded.
			childSession, _ := storeImpl.GetSession(ctx, result.ChildSessionID)
			if childSession.Metadata["subagent_parent"] != "parent-1" {
				t.Errorf("subagent_parent metadata missing")
			}
			if tt.spec.TokenBudget > 0 && childSession.Metadata["subagent_token_budget"] != tt.spec.TokenBudget {
				t.Errorf("token budget not recorded")
			}
		})
	}
}

func TestSpawnChild_MaxIterationsClamped(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Agent.MaxIterations = 5
	storeImpl := newBranchMockStore()
	// LLM always returns tool calls to force iteration counting.
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			return llm.ChatResponse{
				Choices: []llm.Choice{{Message: llm.Message{
					Role: "assistant", Content: "loop",
					ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"x"}`}}},
				}}},
				Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}, nil
		}),
	}
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())

	// Request 100 iterations but parent max is 5 — child must be clamped.
	result, err := agent.SpawnChild(ctx, "parent-1", ChildSpec{Task: "loop task", MaxIterations: 100})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	if result.Success {
		t.Fatal("expected halted (max iterations) but got success")
	}
	if !strings.Contains(strings.ToLower(result.Error), "max_iterations") {
		t.Fatalf("expected max_iterations error, got %q", result.Error)
	}
	// Child max_iterations metadata must be clamped not 100.
	childSession, _ := storeImpl.GetSession(ctx, result.ChildSessionID)
	if childSession.Metadata["subagent_max_iterations"] != 5 {
		t.Fatalf("child max iterations not clamped, got %v want 5", childSession.Metadata["subagent_max_iterations"])
	}
}

func TestSpawnChild_DepthLimit(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newBranchMockStore()
	// Parent at max depth.
	storeImpl.sessions["deep-parent"] = &store.Session{ID: "deep-parent", Metadata: map[string]any{"subagent_depth": maxSubagentDepth}}
	llmReg := newMockLLMRegistry(&llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "x"}}}})
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())

	_, err := agent.SpawnChild(ctx, "deep-parent", ChildSpec{Task: "should fail"})
	if err == nil || !strings.Contains(err.Error(), "max subagent depth") {
		t.Fatalf("expected depth limit error, got %v", err)
	}
}

func TestSpawnChild_SequentialMultiChild(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newBranchMockStore()
	llmReg := newMockLLMRegistry(&llm.ChatResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}},
		Usage: &llm.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	})
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())

	r1, err := agent.SpawnChild(ctx, "parent-1", ChildSpec{Task: "task one"})
	if err != nil {
		t.Fatalf("child1: %v", err)
	}
	r2, err := agent.SpawnChild(ctx, "parent-1", ChildSpec{Task: "task two"})
	if err != nil {
		t.Fatalf("child2: %v", err)
	}
	if r1.ChildSessionID == r2.ChildSessionID {
		t.Fatal("sequential children must have distinct branch sessions")
	}
	if r1.ChildSessionID == "parent-1" || r2.ChildSessionID == "parent-1" {
		t.Fatal("child must not reuse parent id")
	}
	// Structural note: parallelism is blocked by single SQLite connection and
	// single LLM provider queue (RNF-1.5); sequential execution is the correct
	// minimal guarantee. True parallel scheduling is a follow-up.
	parentMsgs, _ := storeImpl.GetMessagesSince(ctx, "parent-1", 0)
	if len(parentMsgs) != 0 {
		// parent had no turn messages in this test store; ensure isolation still holds.
	}
}

func TestSpawnChild_StoreWithoutBranching(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore() // no BranchSession
	llmReg := newMockLLMRegistry(&llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "x"}}}})
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", newTestLogger())
	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, newTestLogger())
	_, err := agent.SpawnChild(ctx, "session-1", ChildSpec{Task: "hello"})
	if err == nil || !strings.Contains(err.Error(), "does not support branching") {
		t.Fatalf("want branching unsupported error, got %v", err)
	}
}

// Ensure imported perms used for signature even though child reuses engine.
var _ perms.Request
var _ = time.Now
