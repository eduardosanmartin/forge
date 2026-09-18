// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestContextAssembler_Build_SystemPromptFirst(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// First message should be system prompt
	if len(messages) == 0 {
		t.Fatal("no messages returned")
	}
	if messages[0].Role != "system" {
		t.Errorf("first message role = %q, want %q", messages[0].Role, "system")
	}
	if messages[0].Content != systemPrompt {
		t.Errorf("first message content doesn't match systemPrompt")
	}
}

// Tool definitions travel exclusively via ToolDefs() (ChatRequest.Tools),
// checked for fixed order in TestContextAssembler_ToolDefs_FixedOrder below.
// Build used to ALSO inject one "TOOL: name - description" system message
// per tool, duplicating the same name+description already in the structured
// schema — this guards against that regressing back in.
func TestContextAssembler_Build_NoDuplicateToolSystemMessages(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.NewDefaultRegistry(nil, "", nil)
	store := &contextMockStore{}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	for _, m := range messages {
		if m.Role == "system" && len(m.Content) > 6 && m.Content[:6] == "TOOL: " {
			t.Errorf("found duplicated plain-text tool description in Build() output: %q", m.Content)
		}
	}
}

func TestContextAssembler_Build_HistoryWindowRespected(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{
		messages: generateMessages(30), // 30 messages = 15 turns
	}
	assembler := NewContextAssembler(toolsReg, store, 5) // max 5 turns = 10 messages

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Count non-system messages (history + user)
	nonSystemCount := 0
	for _, m := range messages {
		if m.Role != "system" {
			nonSystemCount++
		}
	}
	// Should have at most 10 history messages + 1 user message = 11
	// But we also have tool def messages as system messages
	if nonSystemCount > 11 {
		t.Errorf("too many non-system messages: %d (expected <= 11)", nonSystemCount)
	}
}

func TestContextAssembler_Build_AnchoredFactsIncluded(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{
		session: &store.Session{
			ID: "session-1",
			Metadata: map[string]any{
				"anchored_facts": "Project uses Go 1.22. Module: github.com/test/proj",
			},
		},
	}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Find anchored facts message
	found := false
	for _, m := range messages {
		if m.Role == "system" && contains(m.Content, "ANCHORED FACTS") {
			found = true
			if !contains(m.Content, "Project uses Go 1.22") {
				t.Errorf("anchored facts not included correctly: %s", m.Content)
			}
			break
		}
	}
	if !found {
		t.Error("anchored facts message not found")
	}
}

func TestContextAssembler_Build_CurrentUserMessageLast(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "session-1", "my user message")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Last message should be the user message
	lastMsg := messages[len(messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("last message role = %q, want %q", lastMsg.Role, "user")
	}
	if lastMsg.Content != "my user message" {
		t.Errorf("last message content = %q, want %q", lastMsg.Content, "my user message")
	}
}

func TestContextAssembler_ToolDefs_FixedOrder(t *testing.T) {
	toolsReg := tools.NewDefaultRegistry(nil, "", nil)
	store := &contextMockStore{}
	assembler := NewContextAssembler(toolsReg, store, 10)

	toolDefs := assembler.ToolDefs()
	if len(toolDefs) != 10 {
		t.Errorf("expected 10 tool defs, got %d", len(toolDefs))
	}

	expectedOrder := []string{"fs_read", "fs_write", "fs_list", "shell_exec", "git", "git_worktree_add", "git_worktree_list", "git_worktree_remove", "git_branch_task", "github"}
	for i, expected := range expectedOrder {
		if i >= len(toolDefs) {
			t.Errorf("missing tool def at index %d: %s", i, expected)
			continue
		}
		if toolDefs[i].Function.Name != expected {
			t.Errorf("tool def at index %d: name = %q, want %q", i, toolDefs[i].Function.Name, expected)
		}
	}
}

// contextMockStore implements minimal store interface for context testing
type contextMockStore struct {
	session       *store.Session
	messages      []store.Message
	getSessionErr error
}

func (m *contextMockStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	if m.getSessionErr != nil {
		return store.Session{}, m.getSessionErr
	}
	if m.session != nil {
		return *m.session, nil
	}
	return store.Session{}, store.ErrSessionNotFound
}

func (m *contextMockStore) AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error) {
	return 0, 0, nil
}

func (m *contextMockStore) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	if m.messages == nil {
		return []store.Message{}, nil
	}
	// Return newest first (as store does)
	start := offset
	end := offset + limit
	if end > len(m.messages) {
		end = len(m.messages)
	}
	if start >= len(m.messages) {
		return []store.Message{}, nil
	}
	return m.messages[start:end], nil
}

func (m *contextMockStore) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	return []store.Message{}, nil
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestContextAssembler_Build_GetSessionNotFoundProceeds(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{getSessionErr: store.ErrSessionNotFound}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "missing-session", "hello")
	if err != nil {
		t.Fatalf("Build with ErrSessionNotFound should not fail, got %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected messages even when session not found")
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || last.Content != "hello" {
		t.Errorf("last message = %+v, want user/hello", last)
	}
	for _, m := range messages {
		if contains(m.Content, "ANCHORED FACTS") {
			t.Error("anchored facts should not be injected when session not found")
		}
	}
}

func TestContextAssembler_Build_GetSessionWrappedNotFoundProceeds(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	wrapped := fmt.Errorf("wrapped: %w", store.ErrSessionNotFound)
	store := &contextMockStore{getSessionErr: wrapped}
	assembler := NewContextAssembler(toolsReg, store, 10)

	if _, err := assembler.Build(ctx, "missing-session", "hello"); err != nil {
		t.Fatalf("wrapped ErrSessionNotFound should be treated as not-found, got %v", err)
	}
}

func TestContextAssembler_Build_GetSessionOtherErrorFails(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)
	store := &contextMockStore{getSessionErr: fmt.Errorf("db unavailable")}
	assembler := NewContextAssembler(toolsReg, store, 10)

	if _, err := assembler.Build(ctx, "session-1", "hello"); err == nil {
		t.Fatal("expected error when GetSession returns non-not-found error")
	}
}

// TestContextAssembler_Build_WindowNeverOrphansToolMessage guards against a
// bug found running the wordstat example against OpenCode Zen's "Console
// Go": the fixed-size sliding window sliced by raw message count, with no
// awareness that an assistant message carrying ToolCalls and the "tool"
// messages answering each of those calls form an atomic group. When a task
// made several PARALLEL tool calls in one turn and the window boundary fell
// between them, the window kept a later "tool" message while dropping the
// assistant message that declared its ToolCallID — a request with a tool
// result whose id no calling assistant message in the same request. Console
// Go rejected that with HTTP 400 "tool result's tool id ... not found";
// other providers may accept the malformed conversation silently instead.
func TestContextAssembler_Build_WindowNeverOrphansToolMessage(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.New(nil, "", nil)

	// Chronological order (oldest -> newest): assistant makes two parallel
	// tool calls (id1, id2), then the two tool results come back, then a
	// newer user turn. Stored mock convention is newest-first, so this is
	// listed newest (index 0) to oldest (index 3) — mirroring exactly what
	// the real SQLite store's GetMessages returns.
	messages := []store.Message{
		{Role: "user", Content: "next turn"},
		{Role: "tool", ToolCallID: "id2", Content: "result 2"},
		{Role: "tool", ToolCallID: "id1", Content: "result 1"},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "id1", Type: "function", Function: llm.ToolCallFunction{Name: "f"}},
				{ID: "id2", Type: "function", Function: llm.ToolCallFunction{Name: "f"}},
			},
		},
	}
	store := &contextMockStore{messages: messages}
	// maxHistoryTurns=1 -> window=2 messages, keeping only the newest two
	// (indices 0 and 1): the user turn and the tool(id2) result — exactly
	// splitting the assistant+tool_calls group down the middle.
	assembler := NewContextAssembler(toolsReg, store, 1)

	built, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	declared := map[string]bool{}
	for _, m := range built {
		for _, tc := range m.ToolCalls {
			declared[tc.ID] = true
		}
		if m.Role == "tool" && !declared[m.ToolCallID] {
			t.Fatalf("orphaned tool message: ToolCallID %q has no preceding assistant ToolCalls entry in %+v", m.ToolCallID, built)
		}
	}
}

func generateMessages(count int) []store.Message {
	msgs := make([]store.Message, count)
	for i := 0; i < count; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = store.Message{
			ID:        int64(i + 1),
			SessionID: "session-1",
			Seq:       i,
			Role:      role,
			Content:   "message " + string(rune('a'+i%26)),
		}
	}
	return msgs
}
