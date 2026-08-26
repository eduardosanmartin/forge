// Package store implements SQLite-backed session and message persistence for forge.
package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/eduardosanmartin/forge/internal/llm"
)

func TestOpenCreatesFileAndWAL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Verify file exists
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	// Verify WAL mode is enabled
	var journalMode string
	err = s.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode)
	if err != nil {
		t.Fatalf("PRAGMA journal_mode failed: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	// Verify schema_version = 1
	var version int
	err = s.db.QueryRowContext(context.Background(), `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version failed: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d, want 1", version)
	}
}

func TestCreateSession(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	metadata := map[string]any{
		"model":    "qwen2.5-coder:7b",
		"provider": "ollama",
	}
	session, err := s.CreateSession(ctx, metadata)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if session.ID == "" {
		t.Fatal("session ID is empty")
	}
	if session.CreatedAt == 0 {
		t.Fatal("CreatedAt is zero")
	}
	if session.UpdatedAt == 0 {
		t.Fatal("UpdatedAt is zero")
	}
	if session.Metadata == nil {
		t.Fatal("Metadata is nil")
	}
	if session.Metadata["model"] != "qwen2.5-coder:7b" {
		t.Fatalf("metadata model = %v, want qwen2.5-coder:7b", session.Metadata["model"])
	}
}

func TestCreateSessionNilMetadata(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	session, err := s.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateSession with nil metadata failed: %v", err)
	}
	if session.Metadata == nil {
		t.Fatal("Metadata should be empty map, not nil")
	}
}

func TestGetSession(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	created, err := s.CreateSession(ctx, map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	got, err := s.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if got.ID != created.ID {
		t.Fatalf("ID mismatch: got %q, want %q", got.ID, created.ID)
	}
	if got.CreatedAt != created.CreatedAt {
		t.Fatalf("CreatedAt mismatch: got %d, want %d", got.CreatedAt, created.CreatedAt)
	}
	if got.UpdatedAt != created.UpdatedAt {
		t.Fatalf("UpdatedAt mismatch: got %d, want %d", got.UpdatedAt, created.UpdatedAt)
	}
	if got.Metadata["foo"] != "bar" {
		t.Fatalf("metadata foo = %v, want bar", got.Metadata["foo"])
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	_, err := s.GetSession(context.Background(), "non-existent-id")
	if err == nil {
		t.Fatal("expected ErrSessionNotFound, got nil")
	}
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestUpdateSessionMetadata(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, map[string]any{"model": "old"})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Update metadata
	time.Sleep(1 * time.Millisecond)
	err = s.UpdateSessionMetadata(ctx, session.ID, map[string]any{
		"model":    "new",
		"provider": "ollama",
	})
	if err != nil {
		t.Fatalf("UpdateSessionMetadata failed: %v", err)
	}

	// Verify update
	got, err := s.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.Metadata["model"] != "new" {
		t.Fatalf("model = %v, want new", got.Metadata["model"])
	}
	if got.Metadata["provider"] != "ollama" {
		t.Fatalf("provider = %v, want ollama", got.Metadata["provider"])
	}
	// UpdatedAt should have changed
	if got.UpdatedAt <= session.UpdatedAt {
		t.Fatalf("UpdatedAt not updated: got %d, want > %d", got.UpdatedAt, session.UpdatedAt)
	}
}

func TestListSessions(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	// Create multiple sessions with small delays to ensure different timestamps
	for i := 0; i < 5; i++ {
		_, err := s.CreateSession(ctx, map[string]any{"index": i})
		if err != nil {
			t.Fatalf("CreateSession %d failed: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sessions, err := s.ListSessions(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 5 {
		t.Fatalf("expected 5 sessions, got %d", len(sessions))
	}

	// Should be ordered by updated_at DESC (newest first)
	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].UpdatedAt < sessions[i+1].UpdatedAt {
			t.Fatalf("sessions not ordered by updated_at DESC at index %d", i)
		}
	}
}

func TestListSessionsPagination(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := s.CreateSession(ctx, map[string]any{"index": i})
		if err != nil {
			t.Fatalf("CreateSession %d failed: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Page 1: limit 2
	page1, err := s.ListSessions(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListSessions page 1 failed: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1: expected 2, got %d", len(page1))
	}

	// Page 2: limit 2, offset 2
	page2, err := s.ListSessions(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListSessions page 2 failed: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2: expected 2, got %d", len(page2))
	}

	// All IDs should be unique
	seen := make(map[string]bool)
	for _, sess := range append(page1, page2...) {
		if seen[sess.ID] {
			t.Fatalf("duplicate session ID: %s", sess.ID)
		}
		seen[sess.ID] = true
	}
}

func TestDeleteSession(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Add a message
	msg := Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "hello",
	}
	_, _, err = s.AppendMessage(ctx, &msg)
	if err != nil {
		t.Fatalf("AppendMessage failed: %v", err)
	}

	// Delete session
	err = s.DeleteSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Session should be gone
	_, err = s.GetSession(ctx, session.ID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}

	// Messages should be cascade deleted
	msgs, err := s.GetMessages(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages after delete failed: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages after cascade delete, got %d", len(msgs))
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	err := s.DeleteSession(context.Background(), "non-existent")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestAppendAndGetMessages(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Append messages
	messages := []Message{
		{SessionID: session.ID, Role: "user", Content: "Hello"},
		{SessionID: session.ID, Role: "assistant", Content: "Hi there!", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
		{SessionID: session.ID, Role: "user", Content: "How are you?"},
		{SessionID: session.ID, Role: "assistant", Content: "I'm doing well!", Usage: &llm.Usage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12}},
	}

	for i := range messages {
		seq, id, err := s.AppendMessage(ctx, &messages[i])
		if err != nil {
			t.Fatalf("AppendMessage %d failed: %v", i, err)
		}
		if seq != i+1 {
			t.Fatalf("message %d: seq = %d, want %d", i, seq, i+1)
		}
		if id == 0 {
			t.Fatalf("message %d: ID not set", i)
		}
		if messages[i].CreatedAt == 0 {
			t.Fatalf("message %d: CreatedAt not set", i)
		}
		if messages[i].ID != id || messages[i].Seq != seq {
			t.Fatalf("message %d: returned values not reflected in struct", i)
		}
	}

	// Get messages (newest first)
	got, err := s.GetMessages(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got))
	}

	// Verify order (newest first = seq 4, 3, 2, 1)
	for i, msg := range got {
		expectedSeq := 4 - i
		if msg.Seq != expectedSeq {
			t.Fatalf("message index %d: seq = %d, want %d", i, msg.Seq, expectedSeq)
		}
	}

	// Verify content round-trip
	if got[0].Content != "I'm doing well!" {
		t.Fatalf("newest message content = %q, want %q", got[0].Content, "I'm doing well!")
	}
	if got[3].Content != "Hello" {
		t.Fatalf("oldest message content = %q, want %q", got[3].Content, "Hello")
	}

	// Verify usage round-trip
	// got[0] = seq 3 (assistant "I'm doing well!" usage 12)
	// got[1] = seq 2 (user "How are you?" no usage)
	// got[2] = seq 1 (assistant "Hi there!" usage 15)
	// got[3] = seq 0 (user "Hello" no usage)
	if got[0].Usage == nil || got[0].Usage.TotalTokens != 12 {
		t.Fatalf("newest usage = %v, want total 12", got[0].Usage)
	}
	if got[2].Usage == nil || got[2].Usage.TotalTokens != 15 {
		t.Fatalf("assistant usage (seq 1) = %v, want total 15", got[2].Usage)
	}
}

func TestAppendMessageWithToolCalls(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	toolCalls := []llm.ToolCall{
		{ID: "call_1", Type: "function", Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"foo.txt"}`}},
		{ID: "call_2", Type: "function", Function: llm.ToolCallFunction{Name: "write_file", Arguments: `{"path":"bar.txt","content":"hello"}`}},
	}

	msg := Message{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   "I'll read and write files",
		ToolCalls: toolCalls,
	}
	_, _, err = s.AppendMessage(ctx, &msg)
	if err != nil {
		t.Fatalf("AppendMessage with tool_calls failed: %v", err)
	}

	got, err := s.GetMessages(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}

	if len(got[0].ToolCalls) != 2 {
		t.Fatalf("tool_calls count = %d, want 2", len(got[0].ToolCalls))
	}
	if got[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool_call[0].ID = %q, want call_1", got[0].ToolCalls[0].ID)
	}
	if got[0].ToolCalls[1].Function.Name != "write_file" {
		t.Fatalf("tool_call[1].Function.Name = %q, want write_file", got[0].ToolCalls[1].Function.Name)
	}
}

func TestAppendMessageToolResult(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Assistant calls tool
	assistantMsg := Message{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   "Let me check the file",
		ToolCalls: []llm.ToolCall{{ID: "call_1", Type: "function", Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"foo.txt"}`}}},
	}
	_, _, err = s.AppendMessage(ctx, &assistantMsg)
	if err != nil {
		t.Fatalf("AppendMessage assistant failed: %v", err)
	}

	// Tool result
	toolMsg := Message{
		SessionID:  session.ID,
		Role:       "tool",
		Content:    "File contents: hello world",
		ToolCallID: "call_1",
		Name:       "read_file",
	}
	_, _, err = s.AppendMessage(ctx, &toolMsg)
	if err != nil {
		t.Fatalf("AppendMessage tool result failed: %v", err)
	}

	got, err := s.GetMessages(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}

	// Newest first: tool result then assistant
	if got[0].Role != "tool" {
		t.Fatalf("newest role = %q, want tool", got[0].Role)
	}
	if got[0].ToolCallID != "call_1" {
		t.Fatalf("tool ToolCallID = %q, want call_1", got[0].ToolCallID)
	}
	if got[0].Name != "read_file" {
		t.Fatalf("tool Name = %q, want read_file", got[0].Name)
	}
}

func TestGetMessagesSince(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		msg := Message{SessionID: session.ID, Role: "user", Content: "msg " + string(rune('0'+i))}
		_, _, err = s.AppendMessage(ctx, &msg)
		if err != nil {
			t.Fatalf("AppendMessage %d failed: %v", i, err)
		}
	}

	// Get messages since seq 2 (should get seq 3, 4, 5)
	got, err := s.GetMessagesSince(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("GetMessagesSince failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("seqs = [%d..%d], want [3..5]", got[0].Seq, got[2].Seq)
	}

	// Since seq 5 (should get none)
	got, err = s.GetMessagesSince(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("GetMessagesSince seq 4 failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 messages since seq 4, got %d", len(got))
	}
}

func TestAppendMessageEmptySessionID(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	msg := Message{Role: "user", Content: "hello"}
	_, _, err := s.AppendMessage(context.Background(), &msg)
	if !errors.Is(err, ErrEmptySessionID) {
		t.Fatalf("expected ErrEmptySessionID, got %v", err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	const numGoroutines = 10
	const messagesPerGoroutine = 10

	errCh := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			for i := 0; i < messagesPerGoroutine; i++ {
				msg := Message{
					SessionID: session.ID,
					Role:      "user",
					Content:   "goroutine " + string(rune('0'+gid)) + " msg " + string(rune('0'+i)),
				}
				_, _, err := s.AppendMessage(ctx, &msg)
				if err != nil {
					errCh <- err
					return
				}
			}
			errCh <- nil
		}(g)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	// Verify all messages exist
	msgs, err := s.GetMessages(ctx, session.ID, 200, 0)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}
	expected := numGoroutines * messagesPerGoroutine
	if len(msgs) != expected {
		t.Fatalf("expected %d messages, got %d", expected, len(msgs))
	}

	// Verify seqs are unique and sequential
	seen := make(map[int]bool)
	for _, msg := range msgs {
		if seen[msg.Seq] {
			t.Fatalf("duplicate seq: %d", msg.Seq)
		}
		seen[msg.Seq] = true
	}
	for i := 0; i < expected; i++ {
		if !seen[i+1] {
			t.Fatalf("missing seq: %d", i+1)
		}
	}
}

func TestRestartSurvival(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "survival.db")

	// First open: create session and messages
	{
		s, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open 1 failed: %v", err)
		}

		ctx := context.Background()
		session, err := s.CreateSession(ctx, map[string]any{"model": "test"})
		if err != nil {
			t.Fatalf("CreateSession failed: %v", err)
		}

		for i := 0; i < 5; i++ {
			msg := Message{
				SessionID: session.ID,
				Role:      "user",
				Content:   "message " + string(rune('0'+i)),
			}
			_, _, err = s.AppendMessage(ctx, &msg)
			if err != nil {
				t.Fatalf("AppendMessage %d failed: %v", i, err)
			}
		}

		err = s.Close()
		if err != nil {
			t.Fatalf("Close 1 failed: %v", err)
		}
	}

	// Second open: verify data survived
	{
		s, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open 2 failed: %v", err)
		}
		defer s.Close()

		ctx := context.Background()

		// Find the session
		sessions, err := s.ListSessions(ctx, 10, 0)
		if err != nil {
			t.Fatalf("ListSessions failed: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("expected 1 session after restart, got %d", len(sessions))
		}

		session := sessions[0]
		if session.Metadata["model"] != "test" {
			t.Fatalf("metadata model = %v, want test", session.Metadata["model"])
		}

		msgs, err := s.GetMessages(ctx, session.ID, 10, 0)
		if err != nil {
			t.Fatalf("GetMessages failed: %v", err)
		}
		if len(msgs) != 5 {
			t.Fatalf("expected 5 messages after restart, got %d", len(msgs))
		}

		for i, msg := range msgs {
			expectedContent := "message " + string(rune('0'+(4-i))) // newest first
			if msg.Content != expectedContent {
				t.Fatalf("message %d: content = %q, want %q", i, msg.Content, expectedContent)
			}
		}
	}
}

func TestVacuum(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "vacuum.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	ctx := context.Background()

	// Create session and messages
	session, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	for i := 0; i < 100; i++ {
		msg := Message{SessionID: session.ID, Role: "user", Content: "msg"}
		_, _, err = s.AppendMessage(ctx, &msg)
		if err != nil {
			t.Fatalf("AppendMessage %d failed: %v", i, err)
		}
	}

	// Get size before delete
	_, err = s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats before failed: %v", err)
	}

	// Delete session (messages cascade)
	err = s.DeleteSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Size should not have changed yet (space not reclaimed)
	statsAfterDelete, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after delete failed: %v", err)
	}

	// Vacuum
	err = s.Vacuum(ctx)
	if err != nil {
		t.Fatalf("Vacuum failed: %v", err)
	}

	// Close before checking size again (Windows file locking)
	err = s.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Re-open to get fresh stats
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Re-open failed: %v", err)
	}
	defer s2.Close()

	// Size should be reduced after vacuum
	statsAfterVacuum, err := s2.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after vacuum failed: %v", err)
	}

	// Vacuum should reduce file size (at least not increase)
	if statsAfterVacuum.DBSizeBytes > statsAfterDelete.DBSizeBytes {
		t.Fatalf("DB size increased after vacuum: %d -> %d", statsAfterDelete.DBSizeBytes, statsAfterVacuum.DBSizeBytes)
	}

	// Verify data is gone
	msgs, err := s2.GetMessages(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages after vacuum failed: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages after vacuum, got %d", len(msgs))
	}
}

func TestStats(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	// Initial stats
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats initial failed: %v", err)
	}
	if stats.SessionCount != 0 || stats.MessageCount != 0 {
		t.Fatalf("initial stats: sessions=%d, messages=%d, want 0, 0", stats.SessionCount, stats.MessageCount)
	}

	// Add sessions and messages
	for i := 0; i < 3; i++ {
		session, err := s.CreateSession(ctx, nil)
		if err != nil {
			t.Fatalf("CreateSession %d failed: %v", i, err)
		}
		for j := 0; j < 5; j++ {
			msg := Message{SessionID: session.ID, Role: "user", Content: "msg"}
			_, _, err = s.AppendMessage(ctx, &msg)
			if err != nil {
				t.Fatalf("AppendMessage %d failed: %v", j, err)
			}
		}
	}

	stats, err = s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after failed: %v", err)
	}
	if stats.SessionCount != 3 {
		t.Fatalf("SessionCount = %d, want 3", stats.SessionCount)
	}
	if stats.MessageCount != 15 {
		t.Fatalf("MessageCount = %d, want 15", stats.MessageCount)
	}
	if stats.DBSizeBytes <= 0 {
		t.Fatalf("DBSizeBytes = %d, want > 0", stats.DBSizeBytes)
	}
}

func TestContextCancellation(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := s.CreateSession(ctx, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func TestSessionMetadataJSONTypes(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	metadata := map[string]any{
		"string": "value",
		"int":    42,
		"float":  3.14,
		"bool":   true,
		"array":  []string{"a", "b", "c"},
		"object": map[string]any{"nested": "value"},
		"null":   nil,
	}

	session, err := s.CreateSession(ctx, metadata)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	got, err := s.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	// JSON round-trip preserves types
	if got.Metadata["string"] != "value" {
		t.Fatalf("string = %v, want value", got.Metadata["string"])
	}
	if got.Metadata["int"] != float64(42) { // JSON numbers are float64
		t.Fatalf("int = %v, want 42", got.Metadata["int"])
	}
	if got.Metadata["float"] != 3.14 {
		t.Fatalf("float = %v, want 3.14", got.Metadata["float"])
	}
	if got.Metadata["bool"] != true {
		t.Fatalf("bool = %v, want true", got.Metadata["bool"])
	}

	// Array and object should be preserved
	arr, ok := got.Metadata["array"].([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("array = %v, want [a b c]", got.Metadata["array"])
	}
}

func TestListSessionsDefaultLimit(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	ctx := context.Background()

	for i := 0; i < 60; i++ {
		_, err := s.CreateSession(ctx, map[string]any{"index": i})
		if err != nil {
			t.Fatalf("CreateSession %d failed: %v", i, err)
		}
	}

	// Default limit should be 50
	sessions, err := s.ListSessions(ctx, 0, 0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 50 {
		t.Fatalf("default limit: expected 50, got %d", len(sessions))
	}
}

// newTestStore creates a store with a temporary database for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	return s
}
