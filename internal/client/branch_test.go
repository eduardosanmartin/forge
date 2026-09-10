package client

import (
	"context"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestBranchSessionViaClient(t *testing.T) {
	stack := startTestDaemon(t, plainReply("hello"))

	// Create source session via daemon stack.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var src daemon.SessionResult
	if err := stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: map[string]any{"label": "src"}}, &src); err != nil {
		t.Fatalf("create src: %v", err)
	}
	// One turn to generate history (user+assistant).
	var turn daemon.ExecuteTurnResult
	if err := stack.client.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: src.ID, UserMessage: "hi"}, &turn); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if len(turn.Messages) < 2 {
		t.Fatalf("want at least 2 messages in turn, got %d", len(turn.Messages))
	}

	// Branch full.
	branched, err := stack.client.BranchSession(ctx, src.ID, 0, map[string]any{"purpose": "test"})
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	if branched.ID == src.ID {
		t.Fatal("branch id must differ")
	}
	if branched.Metadata["branch_parent"] != src.ID {
		t.Errorf("branch_parent = %v, want %s", branched.Metadata["branch_parent"], src.ID)
	}

	// Verify branched transcript equals source transcript at this point.
	srcMsgs, err := stack.client.GetMessagesSince(ctx, src.ID, 0)
	if err != nil {
		t.Fatalf("get src msgs: %v", err)
	}
	branchMsgs, err := stack.client.GetMessagesSince(ctx, branched.ID, 0)
	if err != nil {
		t.Fatalf("get branch msgs: %v", err)
	}
	if len(branchMsgs.Messages) != len(srcMsgs.Messages) {
		t.Fatalf("branch msgs len %d != src %d", len(branchMsgs.Messages), len(srcMsgs.Messages))
	}

	// Divergence: turn on branch must not affect source.
	if err := stack.client.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: branched.ID, UserMessage: "branch msg"}, &turn); err != nil {
		t.Fatalf("branch turn: %v", err)
	}
	afterSrc, _ := stack.client.GetMessagesSince(ctx, src.ID, 0)
	afterBranch, _ := stack.client.GetMessagesSince(ctx, branched.ID, 0)
	if len(afterSrc.Messages) != len(srcMsgs.Messages) {
		t.Fatalf("src changed after branch turn: got %d want %d", len(afterSrc.Messages), len(srcMsgs.Messages))
	}
	if len(afterBranch.Messages) <= len(srcMsgs.Messages) {
		t.Fatalf("branch did not diverge: got %d want >%d", len(afterBranch.Messages), len(srcMsgs.Messages))
	}

	// Partial branch at seq 1.
	partial, err := stack.client.BranchSession(ctx, src.ID, 1, nil)
	if err != nil {
		t.Fatalf("partial branch: %v", err)
	}
	pMsgs, _ := stack.client.GetMessagesSince(ctx, partial.ID, 0)
	if len(pMsgs.Messages) != 1 {
		t.Fatalf("partial branch at 1: got %d want 1", len(pMsgs.Messages))
	}
}

func TestBranchSessionListShowsBranchParent(t *testing.T) {
	stack := startTestDaemon(t, plainReply("hi"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var src daemon.SessionResult
	_ = stack.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: map[string]any{}}, &src)
	branched, err := stack.client.BranchSession(ctx, src.ID, 0, nil)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	list, err := stack.client.ListSessions(ctx, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, s := range list.Sessions {
		if s.ID == branched.ID {
			if s.Metadata["branch_parent"] != src.ID {
				t.Errorf("list branch_parent = %v want %s", s.Metadata["branch_parent"], src.ID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("branched session not found in list")
	}
}
