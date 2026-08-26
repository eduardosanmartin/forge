// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
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

func TestContextAssembler_Build_ToolDefinitionsInFixedOrder(t *testing.T) {
	ctx := context.Background()
	toolsReg := tools.NewDefaultRegistry(nil, "", nil)
	store := &contextMockStore{}
	assembler := NewContextAssembler(toolsReg, store, 10)

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Find tool definition messages (they have role="system" and content starting with "TOOL:")
	toolMsgs := []llm.Message{}
	for _, m := range messages {
		if m.Role == "system" && len(m.Content) > 6 && m.Content[:6] == "TOOL: " {
			toolMsgs = append(toolMsgs, m)
		}
	}

	if len(toolMsgs) != 5 {
		t.Errorf("expected 5 tool definitions, got %d", len(toolMsgs))
	}

	expectedOrder := []string{"fs.read", "fs.write", "fs.list", "shell.exec", "git"}
	for i, expected := range expectedOrder {
		if i >= len(toolMsgs) {
			t.Errorf("missing tool at index %d: %s", i, expected)
			continue
		}
		// Check that the tool name appears in the message
		if !containsToolName(toolMsgs[i].Content, expected) {
			t.Errorf("tool at index %d: expected %q, got %q", i, expected, toolMsgs[i].Content)
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
	if len(toolDefs) != 5 {
		t.Errorf("expected 5 tool defs, got %d", len(toolDefs))
	}

	expectedOrder := []string{"fs.read", "fs.write", "fs.list", "shell.exec", "git"}
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
	session  *store.Session
	messages []store.Message
}

func (m *contextMockStore) GetSession(ctx context.Context, id string) (store.Session, error) {
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

func containsToolName(content, toolName string) bool {
	// content format: "TOOL: fs.read - ..."
	return findSubstring(content, "TOOL: "+toolName+" -")
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
