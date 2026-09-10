package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestHandlerBranchSession(t *testing.T) {
	t.Run("full copy via handler", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)

		src, err := st.CreateSession(context.Background(), map[string]any{"label": "src"})
		if err != nil {
			t.Fatalf("create src: %v", err)
		}
		if _, _, err := st.AppendMessage(context.Background(), &store.Message{SessionID: src.ID, Role: "user", Content: "hello"}); err != nil {
			t.Fatalf("append: %v", err)
		}
		if _, _, err := st.AppendMessage(context.Background(), &store.Message{SessionID: src.ID, Role: "assistant", Content: "hi"}); err != nil {
			t.Fatalf("append: %v", err)
		}

		req := makeRequest(MethodBranchSession, BranchSessionParams{SourceSessionID: src.ID, AtSeq: 0})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error != nil {
			t.Fatalf("branch error: %+v", resp.Error)
		}
		var res SessionResult
		mustUnmarshal(t, resp.Result, &res)
		if res.Metadata["branch_parent"] != src.ID {
			t.Errorf("branch_parent = %v, want %s", res.Metadata["branch_parent"], src.ID)
		}
		msgs, _ := st.GetMessagesSince(context.Background(), res.ID, 0)
		if len(msgs) != 2 {
			t.Fatalf("branch msgs: got %d want 2", len(msgs))
		}
	})

	t.Run("invalid at_seq", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)
		src, _ := st.CreateSession(context.Background(), nil)
		req := makeRequest(MethodBranchSession, BranchSessionParams{SourceSessionID: src.ID, AtSeq: -1})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
			t.Fatalf("want invalid params, got %+v", resp.Error)
		}
	})

	t.Run("not found", func(t *testing.T) {
		st := newRealStore(t)
		mgr := newBranchMgr(t, st)
		handler := NewHandler(mgr, nil, nil, nil)
		req := makeRequest(MethodBranchSession, BranchSessionParams{SourceSessionID: "nope"})
		resp := handler.HandleRequest(context.Background(), req)
		if resp.Error == nil || resp.Error.Code != ErrCodeSessionNotFound {
			t.Fatalf("want session not found, got %+v", resp.Error)
		}
	})
}

func newRealStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/branch.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newBranchMgr(t *testing.T, st *store.Store) *SessionManager {
	t.Helper()
	mgr := NewSessionManager(st, &branchLLM{}, &branchToolsImpl{}, NewEmergencyState(nil), nil, &config.Config{}, nil, st)
	return mgr
}

type branchLLM struct{}

func (b *branchLLM) GetDefault() (llm.Provider, string) { return nil, "" }
func (b *branchLLM) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, nil
}
func (b *branchLLM) Close() error { return nil }

type branchToolsImpl struct{}

func (b *branchToolsImpl) List() []tools.Tool { return nil }
func (b *branchToolsImpl) Execute(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{}, nil
}

func makeRequest(method string, params any) *JSONRPCRequest {
	raw, _ := json.Marshal(params)
	id := json.RawMessage(`1`)
	return &JSONRPCRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}
}

func mustUnmarshal(t *testing.T, data json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
