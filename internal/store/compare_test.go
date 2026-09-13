package store

import (
	"context"
	"errors"
	"testing"
)

func TestCompareSessionsDivergence(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	parent, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession parent: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := s.AppendMessage(ctx, &Message{SessionID: parent.ID, Role: "user", Content: "p"}); err != nil {
			t.Fatalf("append parent: %v", err)
		}
	}
	// Branch A at seq 1, Branch B at seq 1
	branchA, err := s.BranchSession(ctx, parent.ID, 1, nil)
	if err != nil {
		t.Fatalf("Branch A: %v", err)
	}
	branchB, err := s.BranchSession(ctx, parent.ID, 1, nil)
	if err != nil {
		t.Fatalf("Branch B: %v", err)
	}
	if _, _, err := s.AppendMessage(ctx, &Message{SessionID: branchA.ID, Role: "user", Content: "A1"}); err != nil {
		t.Fatalf("append A1: %v", err)
	}
	if _, _, err := s.AppendMessage(ctx, &Message{SessionID: branchA.ID, Role: "assistant", Content: "A2"}); err != nil {
		t.Fatalf("append A2: %v", err)
	}
	if _, _, err := s.AppendMessage(ctx, &Message{SessionID: branchB.ID, Role: "user", Content: "B1"}); err != nil {
		t.Fatalf("append B1: %v", err)
	}

	cmp, err := s.CompareSessions(ctx, branchA.ID, branchB.ID)
	if err != nil {
		t.Fatalf("CompareSessions: %v", err)
	}
	if cmp.SameSession {
		t.Fatalf("expected not same session")
	}
	if cmp.CountA != 3 {
		t.Fatalf("CountA = %d want 3", cmp.CountA)
	}
	if cmp.CountB != 2 {
		t.Fatalf("CountB = %d want 2", cmp.CountB)
	}
	if cmp.BranchAtSeqA != 1 || cmp.BranchAtSeqB != 1 {
		t.Fatalf("branch_at_seq = %d/%d want 1/1", cmp.BranchAtSeqA, cmp.BranchAtSeqB)
	}
	if cmp.DivergentCountA != 2 {
		t.Fatalf("DivergentCountA = %d want 2", cmp.DivergentCountA)
	}
	if cmp.DivergentCountB != 1 {
		t.Fatalf("DivergentCountB = %d want 1", cmp.DivergentCountB)
	}
	if len(cmp.DivergentA) != 2 || cmp.DivergentA[0].Content != "A1" {
		t.Fatalf("DivergentA = %v want A1,A2", cmp.DivergentA)
	}
	if len(cmp.DivergentB) != 1 || cmp.DivergentB[0].Content != "B1" {
		t.Fatalf("DivergentB = %v want B1", cmp.DivergentB)
	}
	if cmp.BranchParentA != parent.ID || cmp.BranchParentB != parent.ID {
		t.Fatalf("BranchParent = %q/%q want %q", cmp.BranchParentA, cmp.BranchParentB, parent.ID)
	}
}

func TestCompareSessionsSameBranch(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	sess, _ := s.CreateSession(ctx, nil)
	if _, _, err := s.AppendMessage(ctx, &Message{SessionID: sess.ID, Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	cmp, err := s.CompareSessions(ctx, sess.ID, sess.ID)
	if err != nil {
		t.Fatalf("CompareSessions same: %v", err)
	}
	if !cmp.SameSession {
		t.Fatalf("expected SameSession true")
	}
	if cmp.CountA != 1 || cmp.CountB != 1 {
		t.Fatalf("counts = %d/%d want 1/1", cmp.CountA, cmp.CountB)
	}
	if cmp.DivergentCountA != 0 || cmp.DivergentCountB != 0 {
		t.Fatalf("divergent should be 0 for same session, got %d/%d", cmp.DivergentCountA, cmp.DivergentCountB)
	}
	if len(cmp.DivergentA) != 0 || len(cmp.DivergentB) != 0 {
		t.Fatalf("divergent slices should be empty for same session")
	}
}

func TestCompareSessionsNotFound(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	sess, _ := s.CreateSession(ctx, nil)
	if _, err := s.CompareSessions(ctx, sess.ID, "nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound for missing B, got %v", err)
	}
	if _, err := s.CompareSessions(ctx, "nope", sess.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound for missing A, got %v", err)
	}
	if _, err := s.CompareSessions(ctx, "nope", "nope2"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound for both missing, got %v", err)
	}
}
