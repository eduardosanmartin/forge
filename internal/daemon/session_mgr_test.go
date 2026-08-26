// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// Mock store for testing - matches store.Store interface methods we use
type testStore struct {
	sessions map[string]store.Session
	messages map[string][]store.Message
	mu       sync.RWMutex
}

func newTestStore() *testStore {
	return &testStore{
		sessions: make(map[string]store.Session),
		messages: make(map[string][]store.Message),
	}
}

func (m *testStore) CreateSession(ctx context.Context, metadata map[string]any) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "test-session-" + randomID()
	if metadata == nil {
		metadata = make(map[string]any)
	}
	session := store.Session{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		UpdatedAt: time.Now().UnixMilli(),
		Metadata:  metadata,
	}
	m.sessions[id] = session
	m.messages[id] = nil
	return session, nil
}

func (m *testStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return store.Session{}, store.ErrSessionNotFound
	}
	return s, nil
}

func (m *testStore) UpdateSessionMetadata(ctx context.Context, id string, metadata map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return store.ErrSessionNotFound
	}
	for k, v := range metadata {
		s.Metadata[k] = v
	}
	s.UpdatedAt = time.Now().UnixMilli()
	m.sessions[id] = s
	return nil
}

func (m *testStore) ListSessions(ctx context.Context, limit, offset int) ([]store.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var sessions []store.Session
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (m *testStore) DeleteSession(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return store.ErrSessionNotFound
	}
	delete(m.sessions, id)
	delete(m.messages, id)
	return nil
}

func (m *testStore) AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := len(m.messages[msg.SessionID])
	msg.Seq = seq
	msg.ID = int64(seq + 1)
	msg.CreatedAt = time.Now().UnixMilli()
	m.messages[msg.SessionID] = append(m.messages[msg.SessionID], *msg)
	return seq, msg.ID, nil
}

func (m *testStore) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msgs := m.messages[sessionID]
	if msgs == nil {
		return nil, nil
	}
	start := offset
	if start >= len(msgs) {
		return []store.Message{}, nil
	}
	end := start + limit
	if end > len(msgs) {
		end = len(msgs)
	}
	result := make([]store.Message, end-start)
	for i := start; i < end; i++ {
		result[i-start] = msgs[len(msgs)-1-i]
	}
	return result, nil
}

func (m *testStore) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msgs := m.messages[sessionID]
	if msgs == nil {
		return nil, nil
	}
	var result []store.Message
	for _, msg := range msgs {
		if msg.Seq > sinceSeq {
			result = append(result, msg)
		}
	}
	return result, nil
}

func (m *testStore) Close() error                                   { return nil }
func (m *testStore) Vacuum(ctx context.Context) error               { return nil }
func (m *testStore) Stats(ctx context.Context) (store.Stats, error) { return store.Stats{}, nil }

// Mock LLM Registry
type testLLMRegistry struct {
	provider *testProvider
}

func newTestLLMRegistry() *testLLMRegistry {
	return &testLLMRegistry{provider: newTestProvider()}
}

func (m *testLLMRegistry) GetProvider(name string) (llm.Provider, bool) {
	return m.provider, true
}
func (m *testLLMRegistry) GetDefault() (llm.Provider, string) {
	return m.provider, "test-model"
}
func (m *testLLMRegistry) SetDefault(model string) error { return nil }
func (m *testLLMRegistry) ListAll() []llm.ModelInfo      { return nil }
func (m *testLLMRegistry) Close() error                  { return nil }
func (m *testLLMRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return m.provider.Chat(ctx, req)
}
func (m *testLLMRegistry) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return m.provider.ChatStream(ctx, req)
}

type testProvider struct {
	response llm.ChatResponse
}

func newTestProvider() *testProvider {
	return &testProvider{
		response: llm.ChatResponse{
			ID:    "test-response",
			Model: "test-model",
			Choices: []llm.Choice{
				{
					Index: 0,
					Message: llm.Message{
						Role:    "assistant",
						Content: "Test response",
					},
					FinishReason: "stop",
				},
			},
			Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		},
	}
}

func (m *testProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	hasToolResults := false
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			hasToolResults = true
			break
		}
	}
	if hasToolResults {
		return llm.ChatResponse{
			ID:    "test-response-2",
			Model: "test-model",
			Choices: []llm.Choice{
				{
					Index: 0,
					Message: llm.Message{
						Role:    "assistant",
						Content: "Final response after tools",
					},
					FinishReason: "stop",
				},
			},
			Usage: &llm.Usage{PromptTokens: 15, CompletionTokens: 25, TotalTokens: 40},
		}, nil
	}
	return m.response, nil
}
func (m *testProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}
func (m *testProvider) ListModels() ([]string, error) { return []string{"test-model"}, nil }
func (m *testProvider) Close() error                  { return nil }

// Mock Tools Registry
type testToolsRegistry struct {
	tools map[string]tools.Tool
}

func newTestToolsRegistry() *testToolsRegistry {
	return &testToolsRegistry{tools: make(map[string]tools.Tool)}
}

func (m *testToolsRegistry) Register(tool tools.Tool) {
	m.tools[tool.Name()] = tool
}
func (m *testToolsRegistry) Get(name string) (tools.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}
func (m *testToolsRegistry) List() []tools.Tool {
	var list []tools.Tool
	for _, t := range m.tools {
		list = append(list, t)
	}
	return list
}
func (m *testToolsRegistry) Execute(ctx context.Context, name string, args map[string]any) (tools.Result, error) {
	return tools.Result{Content: "tool result for " + name, Metadata: map[string]any{"tool": name}}, nil
}

var idCounter int64

// testPermsEngine is a mock permission engine for testing
type testPermsEngine struct{}

func newTestPermsEngine() *testPermsEngine {
	return &testPermsEngine{}
}

func (e *testPermsEngine) Check(req perms.Request) perms.Decision {
	return perms.Decision{Allowed: true, Rule: "test-allow"}
}

func randomID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&idCounter, 1))
}

func TestSessionManagerCreateSession(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	session, err := mgr.CreateSession(context.Background(), map[string]any{"test": "value"})
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	if session.ID == "" {
		t.Error("expected session ID")
	}
	if session.Metadata["test"] != "value" {
		t.Errorf("expected metadata test=value, got %v", session.Metadata)
	}
}

func TestSessionManagerGetSession(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	created, _ := mgr.CreateSession(context.Background(), nil)
	session, ok := mgr.GetSession(context.Background(), created.ID)
	if !ok {
		t.Fatal("expected session to exist")
	}
	if session.ID != created.ID {
		t.Errorf("expected ID %s, got %s", created.ID, session.ID)
	}

	_, ok = mgr.GetSession(context.Background(), "nonexistent")
	if ok {
		t.Error("expected nonexistent session to return false")
	}
}

func TestSessionManagerListSessions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	mgr.CreateSession(context.Background(), nil)
	mgr.CreateSession(context.Background(), nil)

	sessions, err := mgr.ListSessions(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestSessionManagerDeleteSession(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	created, _ := mgr.CreateSession(context.Background(), nil)
	err := mgr.DeleteSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, ok := mgr.GetSession(context.Background(), created.ID)
	if ok {
		t.Error("expected session to be deleted")
	}
}

func TestSessionManagerHaltResume(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	created, _ := mgr.CreateSession(context.Background(), nil)

	err := mgr.HaltSession(created.ID, "user")
	if err != nil {
		t.Fatalf("halt failed: %v", err)
	}

	if !emergency.IsSessionHalted(created.ID) {
		t.Error("expected session to be halted")
	}

	err = mgr.ResumeSession(created.ID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}

	if emergency.IsSessionHalted(created.ID) {
		t.Error("expected session to be resumed")
	}
}

func TestSessionManagerExecuteTurn(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	session, _ := mgr.CreateSession(context.Background(), nil)

	messages, err := mgr.ExecuteTurn(context.Background(), session.ID, "Hello, world!")
	if err != nil {
		t.Fatalf("execute turn failed: %v", err)
	}

	if len(messages) < 2 {
		t.Errorf("expected at least 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "user" || messages[0].Content != "Hello, world!" {
		t.Errorf("unexpected first message: %+v", messages[0])
	}

	if messages[len(messages)-1].Role != "assistant" {
		t.Errorf("expected last message to be assistant, got %s", messages[len(messages)-1].Role)
	}
}

func TestSessionManagerExecuteTurnHalted(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	session, _ := mgr.CreateSession(context.Background(), nil)

	mgr.HaltSession(session.ID, "user")

	_, err := mgr.ExecuteTurn(context.Background(), session.ID, "Hello")
	if err == nil {
		t.Error("expected error when session halted")
	}
}

func TestSessionManagerGetMessages(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	session, _ := mgr.CreateSession(context.Background(), nil)
	mgr.ExecuteTurn(context.Background(), session.ID, "Test message")

	messages, err := mgr.GetMessages(context.Background(), session.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Error("expected messages")
	}
}

func TestSessionManagerGetMessagesSince(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	store := newTestStore()
	llmReg := newTestLLMRegistry()
	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(logger)
	cfg := config.Defaults()
	permsEng := newTestPermsEngine()

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store)

	session, _ := mgr.CreateSession(context.Background(), nil)
	mgr.ExecuteTurn(context.Background(), session.ID, "Test message")

	messages, err := mgr.GetMessagesSince(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Error("expected messages since seq 0")
	}
}
