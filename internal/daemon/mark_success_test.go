package daemon

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHandler_MarkSuccess(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	ctx := context.Background()

	// Create a session via manager.
	sess, err := h.mgr.CreateSession(ctx, map[string]any{"source": "test"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Mark success via RPC.
	params, _ := json.Marshal(SessionMarkSuccessParams{SessionID: sess.ID})
	req := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSessionMarkSuccess, Params: params}
	resp := h.HandleRequest(ctx, req)
	if resp.Error != nil {
		t.Fatalf("mark success failed: %+v", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["marked"] != true {
		t.Fatalf("expected marked true, got %v", result)
	}

	// Verify metadata.
	got, ok := h.mgr.GetSession(ctx, sess.ID)
	if !ok {
		t.Fatalf("GetSession after mark: not found")
	}
	if got.Metadata["success"] != true {
		t.Fatalf("expected metadata success=true, got %v", got.Metadata)
	}

	// Unknown session should return session not found.
	params2, _ := json.Marshal(SessionMarkSuccessParams{SessionID: "nonexistent"})
	req2 := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSessionMarkSuccess, Params: params2}
	resp2 := h.HandleRequest(ctx, req2)
	if resp2.Error == nil || resp2.Error.Code != ErrCodeSessionNotFound {
		t.Fatalf("expected session not found, got %+v", resp2.Error)
	}

	// Missing session_id should be invalid params.
	params3, _ := json.Marshal(SessionMarkSuccessParams{SessionID: ""})
	req3 := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSessionMarkSuccess, Params: params3}
	resp3 := h.HandleRequest(ctx, req3)
	if resp3.Error == nil || resp3.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected invalid params, got %+v", resp3.Error)
	}
}

func TestSessionManager_MarkSuccess_Errors(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	ctx := context.Background()

	// Not found.
	if err := h.mgr.MarkSuccess(ctx, "nope"); err == nil {
		t.Fatalf("expected error for nonexistent session")
	}

	// Success case sets metadata.
	sess, _ := h.mgr.CreateSession(ctx, nil)
	if err := h.mgr.MarkSuccess(ctx, sess.ID); err != nil {
		t.Fatalf("MarkSuccess: %v", err)
	}
	got, ok := h.mgr.GetSession(ctx, sess.ID)
	if !ok || got.Metadata["success"] != true {
		t.Fatalf("expected success true after mark")
	}
}
