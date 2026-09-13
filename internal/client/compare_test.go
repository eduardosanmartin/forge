package client

import (
	"context"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/store"
)

func TestCompareSessionsViaClient_Divergence(t *testing.T) {
	stack := startTestDaemon(t, plainReply("hello"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var src daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{}, &src); err != nil {
		t.Fatalf("create src: %v", err)
	}
	// Add two messages to src so branch at 1 has a meaningful prefix
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: src.ID, Role: "user", Content: "p1"}); err != nil {
		t.Fatalf("append p1: %v", err)
	}
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: src.ID, Role: "user", Content: "p2"}); err != nil {
		t.Fatalf("append p2: %v", err)
	}
	// Branch two sessions at seq 1
	a, err := stack.client.BranchSession(ctx, src.ID, 1, nil)
	if err != nil {
		t.Fatalf("branch a: %v", err)
	}
	b, err := stack.client.BranchSession(ctx, src.ID, 1, nil)
	if err != nil {
		t.Fatalf("branch b: %v", err)
	}
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: a.ID, Role: "user", Content: "A1"}); err != nil {
		t.Fatalf("append A1: %v", err)
	}
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: a.ID, Role: "assistant", Content: "A2"}); err != nil {
		t.Fatalf("append A2: %v", err)
	}
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: b.ID, Role: "user", Content: "B1"}); err != nil {
		t.Fatalf("append B1: %v", err)
	}
	cmp, err := stack.client.CompareSessions(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.SameSession {
		t.Fatalf("expected not same")
	}
	if cmp.DivergentCountA != 2 || cmp.DivergentCountB != 1 {
		t.Fatalf("divergent counts = %d/%d want 2/1", cmp.DivergentCountA, cmp.DivergentCountB)
	}
	if len(cmp.DivergentA) != 2 || cmp.DivergentA[0].Content != "A1" {
		t.Fatalf("DivergentA = %v want A1", cmp.DivergentA)
	}
	if cmp.BranchAtSeqA != 1 || cmp.BranchAtSeqB != 1 {
		t.Fatalf("branch_at_seq = %d/%d want 1/1", cmp.BranchAtSeqA, cmp.BranchAtSeqB)
	}
}

func TestCompareSessionsViaClient_SameSession(t *testing.T) {
	stack := startTestDaemon(t, plainReply("hi"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var src daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{}, &src); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := stack.store.AppendMessage(ctx, &store.Message{SessionID: src.ID, Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	cmp, err := stack.client.CompareSessions(ctx, src.ID, src.ID)
	if err != nil {
		t.Fatalf("compare same: %v", err)
	}
	if !cmp.SameSession {
		t.Fatalf("expected SameSession true")
	}
	if cmp.CountA != 1 || cmp.DivergentCountA != 0 {
		t.Fatalf("same: count=%d divergent=%d want 1/0", cmp.CountA, cmp.DivergentCountA)
	}
}

func TestCompareSessionsViaClient_NotFound(t *testing.T) {
	stack := startTestDaemon(t, plainReply("hi"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var src daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{}, &src); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := stack.client.CompareSessions(ctx, src.ID, "nope")
	if err == nil {
		t.Fatalf("expected error for not found")
	}
	if !IsCode(err, daemon.ErrCodeSessionNotFound) {
		t.Fatalf("expected ErrCodeSessionNotFound, got %v", err)
	}
}


