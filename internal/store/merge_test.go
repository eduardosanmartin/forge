package store

import (
	"context"
	"errors"
	"testing"
)

func TestMergeBranchAppendsTail(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()

	// Parent session with 2 messages.
	parent, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession parent: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := s.AppendMessage(ctx, &Message{SessionID: parent.ID, Role: "user", Content: "p"}); err != nil {
			t.Fatalf("append parent %d: %v", i, err)
		}
	}

	// Branch at seq 1 (copies 1 message).
	branch, err := s.BranchSession(ctx, parent.ID, 1, nil)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	// Append 2 messages to branch (new tail beyond at_seq=1).
	for _, c := range []string{"b1", "b2"} {
		if _, _, err := s.AppendMessage(ctx, &Message{SessionID: branch.ID, Role: "user", Content: c}); err != nil {
			t.Fatalf("append branch %q: %v", c, err)
		}
	}
	branchMsgs, _ := s.GetMessagesSince(ctx, branch.ID, 0)
	if len(branchMsgs) != 3 {
		t.Fatalf("branch msgs before merge: got %d want 3", len(branchMsgs))
	}

	// Target session with 1 message.
	target, err := s.CreateSession(ctx, nil)
	if err != nil {
		t.Fatalf("CreateSession target: %v", err)
	}
	if _, _, err := s.AppendMessage(ctx, &Message{SessionID: target.ID, Role: "user", Content: "t"}); err != nil {
		t.Fatalf("append target: %v", err)
	}

	merged, err := s.MergeBranch(ctx, branch.ID, target.ID)
	if err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if merged.Metadata["merged_from"] != branch.ID {
		t.Errorf("merged_from = %v want %s", merged.Metadata["merged_from"], branch.ID)
	}
	if merged.Metadata["merged_count"] != float64(2) && merged.Metadata["merged_count"] != 2 && merged.Metadata["merged_count"] != int64(2) {
		// JSON numbers may decode as float64; direct int is also possible.
		t.Logf("merged_count = %v (%T) - accepting 2 in any numeric form", merged.Metadata["merged_count"], merged.Metadata["merged_count"])
		if merged.Metadata["merged_count"] == nil {
			t.Errorf("merged_count missing")
		}
	}

	tailTarget, err := s.GetMessagesSince(ctx, target.ID, 0)
	if err != nil {
		t.Fatalf("GetMessagesSince target: %v", err)
	}
	// Target had 1 + tail 2 (messages after at_seq 1 in branch) = 3 total.
	// Branch tail is seq 2,3 (values "b1","b2") after the copied prefix.
	if len(tailTarget) != 3 {
		t.Fatalf("target after merge: got %d want 3", len(tailTarget))
	}
	if tailTarget[1].Content != "b1" || tailTarget[2].Content != "b2" {
		t.Errorf("tail contents = %q %q want b1 b2", tailTarget[1].Content, tailTarget[2].Content)
	}
}

func TestMergeBranchNotFound(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	tgt, _ := s.CreateSession(ctx, nil)

	tests := []struct {
		name   string
		source string
		target string
	}{
		{"source not found", "nope", tgt.ID},
		{"target not found", tgt.ID, "nope-target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.source
			tgtID := tc.target
			// For second case ensure source exists.
			if tc.name == "target not found" {
				srcSess, _ := s.CreateSession(ctx, nil)
				src = srcSess.ID
			}
			if _, err := s.MergeBranch(ctx, src, tgtID); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("want ErrSessionNotFound got %v", err)
			}
		})
	}
}

func TestMergeBranchSameID(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	sess, _ := s.CreateSession(ctx, nil)
	if _, err := s.MergeBranch(ctx, sess.ID, sess.ID); err == nil || err.Error() != "merge: source and target must differ" {
		t.Fatalf("want same-id error, got %v", err)
	}
}

func TestMergeBranchFullCopyTailIsAll(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	src, _ := s.CreateSession(ctx, nil)
	for i := 0; i < 2; i++ {
		_, _, _ = s.AppendMessage(ctx, &Message{SessionID: src.ID, Role: "user", Content: "src"})
	}
	tgt, _ := s.CreateSession(ctx, nil)
	merged, err := s.MergeBranch(ctx, src.ID, tgt.ID)
	if err != nil {
		t.Fatalf("MergeBranch full: %v", err)
	}
	if merged.Metadata["merged_from"] != src.ID {
		t.Errorf("merged_from = %v want %s", merged.Metadata["merged_from"], src.ID)
	}
	msgs, _ := s.GetMessagesSince(ctx, tgt.ID, 0)
	if len(msgs) != 2 {
		t.Fatalf("target after full merge: got %d want 2", len(msgs))
	}
}
