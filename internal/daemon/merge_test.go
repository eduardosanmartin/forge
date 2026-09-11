package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eduardosanmartin/forge/internal/store"
)

func TestHandlerMergeSession(t *testing.T) {
	t.Run("append tail via handler", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)

		src, err := st.CreateSession(context.Background(), nil)
		if err != nil {
			t.Fatalf("create src: %v", err)
		}
		if _, _, err := st.AppendMessage(context.Background(), &store.Message{SessionID: src.ID, Role: "user", Content: "hello"}); err != nil {
			t.Fatalf("append: %v", err)
		}
		branch, err := st.BranchSession(context.Background(), src.ID, 1, nil)
		if err != nil {
			t.Fatalf("branch: %v", err)
		}
		if _, _, err := st.AppendMessage(context.Background(), &store.Message{SessionID: branch.ID, Role: "user", Content: "branch-tail"}); err != nil {
			t.Fatalf("append tail: %v", err)
		}
		target, err := st.CreateSession(context.Background(), nil)
		if err != nil {
			t.Fatalf("create target: %v", err)
		}

		req := makeRequest(MethodMergeSession, MergeSessionParams{SourceSessionID: branch.ID, TargetSessionID: target.ID})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error != nil {
			t.Fatalf("merge error: %+v", resp.Error)
		}
		var res SessionResult
		mustUnmarshal(t, resp.Result, &res)
		if res.Metadata["merged_from"] != branch.ID {
			t.Errorf("merged_from = %v want %s", res.Metadata["merged_from"], branch.ID)
		}
		msgs, _ := st.GetMessagesSince(context.Background(), target.ID, 0)
		if len(msgs) != 1 {
			t.Fatalf("target msgs: got %d want 1", len(msgs))
		}
		if msgs[0].Content != "branch-tail" {
			t.Errorf("tail content = %q want branch-tail", msgs[0].Content)
		}
		// Also verify via RPC unmarshal shape.
		var probe map[string]any
		_ = json.Unmarshal(resp.Result, &probe)
		if _, ok := probe["id"]; !ok {
			t.Error("result missing id")
		}
	})

	t.Run("not found", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)
		req := makeRequest(MethodMergeSession, MergeSessionParams{SourceSessionID: "nope", TargetSessionID: "nope2"})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error == nil || resp.Error.Code != ErrCodeSessionNotFound {
			t.Fatalf("want session not found, got %+v", resp.Error)
		}
	})

	t.Run("missing params", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)
		src, _ := st.CreateSession(context.Background(), nil)
		req := makeRequest(MethodMergeSession, MergeSessionParams{SourceSessionID: src.ID, TargetSessionID: ""})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
			t.Fatalf("want invalid params, got %+v", resp.Error)
		}
	})
}
