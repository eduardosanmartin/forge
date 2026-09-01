package client

import (
	"bytes"
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestREPL_SuccessCommand(t *testing.T) {
	stack := startTestDaemon(t)
	ctx, cancel := callCtx(t)
	defer cancel()

	// Create a session via RPC.
	var sess daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: map[string]any{}}, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	repl := NewREPL(stack.client, sess.ID, &bytes.Buffer{}, bytes.NewReader(nil), REPLOptions{})
	// Directly call cmdSuccess with current session.
	// We capture output via repl.out.
	buf := &bytes.Buffer{}
	repl.out = buf
	repl.cmdSuccess(ctx, "/success")
	if !contains(buf.String(), "marked as successful") {
		t.Fatalf("expected success message, got %q", buf.String())
	}

	// Verify metadata.
	var got daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodGetSession, daemon.GetSessionParams{SessionID: sess.ID}, &got); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Metadata["success"] != true {
		t.Fatalf("expected success true, got %v", got.Metadata["success"])
	}

	// Test with explicit id.
	stack2 := startTestDaemon(t)
	ctx2, cancel2 := callCtx(t)
	defer cancel2()
	var sess2 daemon.SessionResult
	if err := stack2.client.Call(ctx2, daemon.MethodCreateSession, nil, &sess2); err != nil {
		t.Fatalf("create sess2: %v", err)
	}
	repl2 := NewREPL(stack2.client, "other-id", &bytes.Buffer{}, bytes.NewReader(nil), REPLOptions{})
	buf2 := &bytes.Buffer{}
	repl2.out = buf2
	repl2.cmdSuccess(ctx2, "/success "+sess2.ID)
	if !contains(buf2.String(), "marked as successful") {
		t.Fatalf("explicit success failed: %q", buf2.String())
	}

	// Unknown session should error.
	buf3 := &bytes.Buffer{}
	repl3 := NewREPL(stack.client, sess.ID, buf3, bytes.NewReader(nil), REPLOptions{})
	repl3.cmdSuccess(ctx, "/success nonexistent-id-123")
	if !contains(buf3.String(), "error:") {
		t.Fatalf("expected error for unknown session, got %q", buf3.String())
	}
}

func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

func TestClient_MarkSuccess(t *testing.T) {
	stack := startTestDaemon(t)
	ctx, cancel := callCtx(t)
	defer cancel()

	var sess daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, nil, &sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := stack.client.MarkSuccess(ctx, sess.ID); err != nil {
		t.Fatalf("MarkSuccess: %v", err)
	}

	// Verify via GetSession.
	got, err := stack.client.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Metadata["success"] != true {
		t.Fatalf("expected success true, got %v", got.Metadata)
	}

	// MarkSuccess on unknown should fail.
	if err := stack.client.MarkSuccess(ctx, "nope"); err == nil {
		t.Fatalf("expected error for unknown session")
	} else if !IsCode(err, daemon.ErrCodeSessionNotFound) {
		t.Fatalf("expected session not found code, got %v", err)
	}
}

func TestClient_ListAndGetMessages(t *testing.T) {
	stack := startTestDaemon(t)
	ctx, cancel := callCtx(t)
	defer cancel()

	var sess daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, nil, &sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := stack.client.ListSessions(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Sessions) == 0 {
		t.Fatalf("expected at least 1 session")
	}

	msgs, err := stack.client.GetMessages(ctx, sess.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if msgs == nil {
		t.Fatalf("expected messages result not nil")
	}
	// Empty session should return 0 messages (no error).
	if len(msgs.Messages) != 0 {
		t.Fatalf("expected 0 messages for new session, got %d", len(msgs.Messages))
	}

	// Ensure context import used.
	_ = context.Background()
}
