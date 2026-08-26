// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// testPermsPolicy returns a permissive permissions policy for testing
func testPermsPolicy() perms.PermissionsPolicy {
	return perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"*"}},
		Git:   perms.GitPermissions{Allow: []string{"*"}},
	}
}

// newTestPermsEngine creates a permissive perms engine for testing
func newTestPermsEngine(t *testing.T) *perms.Engine {
	t.Helper()
	eng, err := perms.New(testPermsPolicy(), "C:\\", nil) // Use C:\ as workspace root on Windows
	if err != nil {
		t.Fatalf("create perms engine: %v", err)
	}
	return eng
}

func TestAgent_ExecuteTurn_SingleTurnNoTools(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	llmReg := newMockLLMRegistry(&llm.ChatResponse{
		Choices: []llm.Choice{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Hello! How can I help you?",
				},
			},
		},
		Usage: &llm.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	})
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	result, err := agent.ExecuteTurn(ctx, "session-1", "Hello")
	if err != nil {
		t.Fatalf("ExecuteTurn failed: %v", err)
	}

	// Should have user message + assistant message
	if len(result.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("first message role = %q, want %q", result.Messages[0].Role, "user")
	}
	if result.Messages[1].Role != "assistant" {
		t.Errorf("second message role = %q, want %q", result.Messages[1].Role, "assistant")
	}

	// Metrics should be populated
	if result.Metrics.IterationCount != 1 {
		t.Errorf("IterationCount = %d, want 1", result.Metrics.IterationCount)
	}
	if result.Metrics.ToolCallCount != 0 {
		t.Errorf("ToolCallCount = %d, want 0", result.Metrics.ToolCallCount)
	}
	if result.Metrics.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", result.Metrics.TotalTokens)
	}
	if result.Metrics.LLMTimeMs <= 0 {
		t.Errorf("LLMTimeMs = %d, want > 0", result.Metrics.LLMTimeMs)
	}
	if result.Metrics.HarnessOverheadMs < 0 {
		t.Errorf("HarnessOverheadMs = %d, want >= 0", result.Metrics.HarnessOverheadMs)
	}
	if result.Halted {
		t.Error("Halted should be false")
	}
}

func TestAgent_ExecuteTurn_MultiTurnWithToolCalls(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	// First response: tool call
	// Second response: final answer
	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return llm.ChatResponse{
					Choices: []llm.Choice{
						{
							Message: llm.Message{
								Role:    "assistant",
								Content: "I'll read that file for you.",
								ToolCalls: []llm.ToolCall{
									{
										ID:   "call-1",
										Type: "function",
										Function: llm.ToolCallFunction{
											Name:      "fs.read",
											Arguments: `{"path": "test.txt"}`,
										},
									},
								},
							},
						},
					},
					Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
				}, nil
			}
			return llm.ChatResponse{
				Choices: []llm.Choice{
					{
						Message: llm.Message{
							Role:    "assistant",
							Content: "The file contains: Hello World",
						},
					},
				},
				Usage: &llm.Usage{PromptTokens: 150, CompletionTokens: 20, TotalTokens: 170},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	result, err := agent.ExecuteTurn(ctx, "session-1", "Read test.txt")
	if err != nil {
		t.Fatalf("ExecuteTurn failed: %v", err)
	}

	// Should have: user, assistant(with tool call), tool result, assistant(final)
	if len(result.Messages) != 4 {
		t.Errorf("expected 4 messages, got %d: %+v", len(result.Messages), result.Messages)
		for i, m := range result.Messages {
			t.Logf("  [%d] role=%s content=%s", i, m.Role, m.Content)
		}
	}

	if result.Messages[0].Role != "user" {
		t.Errorf("msg[0] role = %q", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "assistant" || len(result.Messages[1].ToolCalls) == 0 {
		t.Errorf("msg[1] should be assistant with tool calls")
	}
	if result.Messages[2].Role != "tool" {
		t.Errorf("msg[2] role = %q, want %q", result.Messages[2].Role, "tool")
	}
	if result.Messages[3].Role != "assistant" {
		t.Errorf("msg[3] role = %q, want %q", result.Messages[3].Role, "assistant")
	}

	// Metrics
	if result.Metrics.IterationCount != 2 {
		t.Errorf("IterationCount = %d, want 2", result.Metrics.IterationCount)
	}
	if result.Metrics.ToolCallCount != 1 {
		t.Errorf("ToolCallCount = %d, want 1", result.Metrics.ToolCallCount)
	}
	if result.Metrics.TotalTokens != 300 { // 130 + 170
		t.Errorf("TotalTokens = %d, want 300", result.Metrics.TotalTokens)
	}
}

func TestAgent_ExecuteTurn_MaxIterationsEnforced(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	// Always return tool calls - should hit max iterations
	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			return llm.ChatResponse{
				Choices: []llm.Choice{
					{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Let me try again",
							ToolCalls: []llm.ToolCall{
								{
									ID:   "call-" + string(rune('0'+callCount)),
									Type: "function",
									Function: llm.ToolCallFunction{
										Name:      "fs.read",
										Arguments: `{"path": "test.txt"}`,
									},
								},
							},
						},
					},
				},
				Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)
	// Override maxIterations for testing
	agent.maxIterations = 3

	result, err := agent.ExecuteTurn(ctx, "session-1", "Do something")
	if err == nil {
		t.Error("expected error when max iterations exceeded")
	}
	if !result.Halted {
		t.Error("Halted should be true when max iterations exceeded")
	}
	// Max iterations check happens after increment, so with maxIterations=3,
	// we get 4 iterations (1,2,3,4 where 4 exceeds the limit)
	if result.Metrics.IterationCount != 4 {
		t.Errorf("IterationCount = %d, want 4", result.Metrics.IterationCount)
	}
}

func TestAgent_ExecuteTurn_HaltedSessionReturnsEarly(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	// Pre-mark session as halted
	storeImpl.sessions["session-1"] = &store.Session{
		ID: "session-1",
		Metadata: map[string]any{
			"halted":      true,
			"halt_reason": "user requested",
		},
	}

	llmReg := newMockLLMRegistry(nil) // Should not be called
	toolsReg := tools.NewDefaultRegistry(nil, "", nil)
	permsEng := newTestPermsEngine(t)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	result, err := agent.ExecuteTurn(ctx, "session-1", "Hello")
	if err == nil {
		t.Error("expected error for halted session")
	}
	if !result.Halted {
		t.Error("Halted should be true")
	}
	if result.Metrics.IterationCount != 0 {
		t.Errorf("IterationCount = %d, want 0", result.Metrics.IterationCount)
	}
}

func TestAgent_ExecuteTurn_MetricsRecordedCorrectly(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			// Simulate some LLM latency
			time.Sleep(5 * time.Millisecond)
			return llm.ChatResponse{
				Choices: []llm.Choice{
					{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Done",
						},
					},
				},
				Usage: &llm.Usage{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	result, err := agent.ExecuteTurn(ctx, "session-1", "Hello")
	if err != nil {
		t.Fatalf("ExecuteTurn failed: %v", err)
	}

	// Verify metrics structure
	if result.Metrics.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}
	if result.Metrics.EndTime.IsZero() {
		t.Error("EndTime should be set")
	}
	if result.Metrics.LLMTimeMs <= 0 {
		t.Errorf("LLMTimeMs = %d, want > 0", result.Metrics.LLMTimeMs)
	}
	if result.Metrics.HarnessOverheadMs < 0 {
		t.Errorf("HarnessOverheadMs = %d, want >= 0", result.Metrics.HarnessOverheadMs)
	}
	if result.Metrics.PromptTokens != 200 {
		t.Errorf("PromptTokens = %d, want 200", result.Metrics.PromptTokens)
	}
	if result.Metrics.CompletionTokens != 100 {
		t.Errorf("CompletionTokens = %d, want 100", result.Metrics.CompletionTokens)
	}
	if result.Metrics.TotalTokens != 300 {
		t.Errorf("TotalTokens = %d, want 300", result.Metrics.TotalTokens)
	}
	if result.Metrics.DurationMs() <= 0 {
		t.Errorf("DurationMs() = %d, want > 0", result.Metrics.DurationMs())
	}
}

func TestAgent_ConcurrentExecuteTurn_DifferentSessions(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()

	callCount := 0
	llmReg := &mockLLMRegistry{
		provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
			callCount++
			return llm.ChatResponse{
				Choices: []llm.Choice{
					{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Response " + string(rune('0'+callCount)),
						},
					},
				},
				Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			}, nil
		}),
	}

	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	// Run concurrent turns on different sessions
	results := make(chan TurnResult, 2)

	go func() {
		r, _ := agent.ExecuteTurn(ctx, "session-A", "Hello A")
		results <- r
	}()

	go func() {
		r, _ := agent.ExecuteTurn(ctx, "session-B", "Hello B")
		results <- r
	}()

	// Wait for both
	var r1, r2 TurnResult
	select {
	case r1 = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for result 1")
	}
	select {
	case r2 = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for result 2")
	}

	if r1.Messages[len(r1.Messages)-1].Content != "Response 1" && r1.Messages[len(r1.Messages)-1].Content != "Response 2" {
		t.Errorf("unexpected response content: %s", r1.Messages[len(r1.Messages)-1].Content)
	}
	if r2.Messages[len(r2.Messages)-1].Content != "Response 1" && r2.Messages[len(r2.Messages)-1].Content != "Response 2" {
		t.Errorf("unexpected response content: %s", r2.Messages[len(r2.Messages)-1].Content)
	}
	// Both should succeed
	if r1.Halted || r2.Halted {
		t.Error("neither session should be halted")
	}
}

func TestAgent_ExecuteTurn_SessionNotFound(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	storeImpl := newMockStore()
	llmReg := newMockLLMRegistry(nil)
	permsEng := newTestPermsEngine(t)
	toolsReg := tools.NewDefaultRegistry(permsEng, "", nil)
	logger := newTestLogger()

	agent := NewAgent(cfg, storeImpl, llmReg, toolsReg, permsEng, logger)

	result, err := agent.ExecuteTurn(ctx, "nonexistent", "Hello")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
	if !result.Halted {
		t.Error("Halted should be true")
	}
}

// Mock implementations

// mockProvider implements llm.Provider for testing
type mockProvider struct {
	chatFunc func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

func newMockProvider(chatFunc func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)) *mockProvider {
	return &mockProvider{chatFunc: chatFunc}
}

func (m *mockProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	// Simulate minimal network latency
	time.Sleep(time.Millisecond)
	if m.chatFunc != nil {
		return m.chatFunc(ctx, req)
	}
	return llm.ChatResponse{}, errors.New("not implemented")
}

func (m *mockProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (m *mockProvider) ListModels() ([]string, error) {
	return []string{"test-model"}, nil
}

func (m *mockProvider) Close() error {
	return nil
}

type mockStore struct {
	sessions map[string]*store.Session
	messages map[string][]store.Message
}

func newMockStore() *mockStore {
	return &mockStore{
		sessions: map[string]*store.Session{
			"session-1": {ID: "session-1", Metadata: map[string]any{}},
		},
		messages: make(map[string][]store.Message),
	}
}

func (m *mockStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	if s, ok := m.sessions[id]; ok {
		return *s, nil
	}
	// Auto-create session for testing
	newSession := &store.Session{
		ID:        id,
		Metadata:  map[string]any{},
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
	}
	m.sessions[id] = newSession
	m.messages[id] = []store.Message{}
	return *newSession, nil
}

func (m *mockStore) AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error) {
	seq := len(m.messages[msg.SessionID])
	msg.Seq = seq
	msg.ID = int64(seq + 1)
	msg.CreatedAt = time.Now().UnixMilli()
	m.messages[msg.SessionID] = append(m.messages[msg.SessionID], *msg)
	return seq, msg.ID, nil
}

func (m *mockStore) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	msgs := m.messages[sessionID]
	if msgs == nil {
		return []store.Message{}, nil
	}
	// Return newest first
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

func (m *mockStore) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	msgs := m.messages[sessionID]
	if msgs == nil {
		return []store.Message{}, nil
	}
	var result []store.Message
	for _, msg := range msgs {
		if msg.Seq > sinceSeq {
			result = append(result, msg)
		}
	}
	return result, nil
}

func (m *mockStore) CreateSession(ctx context.Context, metadata map[string]any) (store.Session, error) {
	return store.Session{}, nil
}
func (m *mockStore) UpdateSessionMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return nil
}
func (m *mockStore) ListSessions(ctx context.Context, limit, offset int) ([]store.Session, error) {
	return nil, nil
}
func (m *mockStore) DeleteSession(ctx context.Context, id string) error { return nil }
func (m *mockStore) Close() error                                       { return nil }

type mockLLMRegistry struct {
	provider *mockProvider
}

func newMockLLMRegistry(resp *llm.ChatResponse) *mockLLMRegistry {
	if resp != nil {
		return &mockLLMRegistry{
			provider: newMockProvider(func(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
				return *resp, nil
			}),
		}
	}
	return &mockLLMRegistry{
		provider: newMockProvider(nil),
	}
}

func (m *mockLLMRegistry) GetDefault() (llm.Provider, string) {
	return m.provider, "test-model"
}
func (m *mockLLMRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return m.provider.Chat(ctx, req)
}
func (m *mockLLMRegistry) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return m.provider.ChatStream(ctx, req)
}
func (m *mockLLMRegistry) Close() error { return m.provider.Close() }

type mockPermsEngine struct{}

func newMockPermsEngine() *mockPermsEngine {
	return &mockPermsEngine{}
}

func (m *mockPermsEngine) Check(req perms.Request) perms.Decision {
	return perms.Decision{Allowed: true, Rule: "test-allow"}
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, nil))
}
