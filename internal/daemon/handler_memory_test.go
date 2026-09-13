package daemon

import (
	"context"
	"testing"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/store"
)

// newAnchorWiredHandler builds a handler over a temp SQLite store with the
// anchors table created and an AnchorStoreSQL wired via WithV1Deps.
func newAnchorWiredHandler(t *testing.T) (*store.Store, *Handler) {
	t.Helper()
	ctx := context.Background()
	st := newRealStore(t)
	if err := anchor.CreateAnchorTable(ctx, st.DB()); err != nil {
		t.Fatalf("create anchor table: %v", err)
	}
	anchorStore := anchor.NewAnchorStoreSQL(st.DB())
	mgr := NewSessionManager(st, &branchLLM{}, &branchToolsImpl{}, NewEmergencyState(nil), nil, nil, nil, st,
		WithV1Deps(agent.V1Deps{AnchorStore: anchorStore}))
	return st, NewHandler(mgr, nil, nil, nil)
}

func TestHandlerMemoryRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("sqlite round-trip skipped in -short")
	}
	_, handler := newAnchorWiredHandler(t)
	ctx := context.Background()

	// create (no session -> "global" convention, with tags)
	req := makeRequest(MethodMemoryCreate, MemoryCreateParams{Content: "prefer table tests", Source: "user", Tags: []string{"testing", "style"}})
	res := handler.HandleRequest(ctx, req)
	if res.Error != nil {
		t.Fatalf("create error: %+v", res.Error)
	}
	var created MemoryResult
	mustUnmarshal(t, res.Result, &created)
	if created.Anchor.ID == 0 {
		t.Fatal("create must return an id")
	}
	if created.Anchor.SessionID != "global" || created.Anchor.Source != "user" {
		t.Fatalf("defaults wrong: %+v", created.Anchor)
	}
	if len(created.Anchor.Tags) != 2 {
		t.Fatalf("tags not persisted: %+v", created.Anchor.Tags)
	}
	id := created.Anchor.ID

	// create in a real session for the filter test
	req = makeRequest(MethodMemoryCreate, MemoryCreateParams{Content: "session-scoped fact", SessionID: "sess-1"})
	res = handler.HandleRequest(ctx, req)
	if res.Error != nil {
		t.Fatalf("create error: %+v", res.Error)
	}

	// list (all)
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryList, MemoryListParams{}))
	if res.Error != nil {
		t.Fatalf("list error: %+v", res.Error)
	}
	var listed MemoryListResult
	mustUnmarshal(t, res.Result, &listed)
	if len(listed.Anchors) != 2 {
		t.Fatalf("list all = %d anchors, want 2: %+v", len(listed.Anchors), listed.Anchors)
	}

	// list filtered by session
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryList, MemoryListParams{SessionID: "sess-1"}))
	mustUnmarshal(t, res.Result, &listed)
	if len(listed.Anchors) != 1 || listed.Anchors[0].Content != "session-scoped fact" {
		t.Fatalf("filtered list wrong: %+v", listed.Anchors)
	}

	// get
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryGet, MemoryGetParams{ID: id}))
	if res.Error != nil {
		t.Fatalf("get error: %+v", res.Error)
	}
	var got MemoryResult
	mustUnmarshal(t, res.Result, &got)
	if got.Anchor.Content != "prefer table tests" || got.Anchor.ID != id {
		t.Fatalf("get wrong anchor: %+v", got.Anchor)
	}

	// update: patch content and tags only; source must stay untouched (nil = no change)
	newContent := "prefer behavior assertions"
	newTags := []string{"updated"}
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryUpdate, MemoryUpdateParams{ID: id, Content: &newContent, Tags: &newTags}))
	if res.Error != nil {
		t.Fatalf("update error: %+v", res.Error)
	}
	var updated MemoryResult
	mustUnmarshal(t, res.Result, &updated)
	if updated.Anchor.Content != newContent {
		t.Fatalf("content not updated: %+v", updated.Anchor)
	}
	if updated.Anchor.Source != "user" {
		t.Fatalf("source changed on partial update: %+v", updated.Anchor)
	}
	if len(updated.Anchor.Tags) != 1 || updated.Anchor.Tags[0] != "updated" {
		t.Fatalf("tags not replaced: %+v", updated.Anchor.Tags)
	}
	if updated.Anchor.UpdatedAt < updated.Anchor.CreatedAt {
		// Same-millisecond updates keep the equality; the invariant is that
		// updated_at never predates created_at.
		t.Fatalf("updated_at predates created_at: %+v", updated.Anchor)
	}

	// delete
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryDelete, MemoryDeleteParams{ID: id}))
	if res.Error != nil {
		t.Fatalf("delete error: %+v", res.Error)
	}

	// get after delete -> not found
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryGet, MemoryGetParams{ID: id}))
	if res.Error == nil || res.Error.Code != ErrCodeJobNotFound {
		t.Fatalf("get deleted anchor: want not-found, got %+v", res.Error)
	}
	// delete again -> not found
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryDelete, MemoryDeleteParams{ID: id}))
	if res.Error == nil || res.Error.Code != ErrCodeJobNotFound {
		t.Fatalf("delete deleted anchor: want not-found, got %+v", res.Error)
	}

	// update without any fields is a no-op that still bumps and returns the anchor
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryGet, MemoryGetParams{ID: 2}))
	if res.Error != nil {
		t.Fatalf("anchor 2 missing: %+v", res.Error)
	}
	res = handler.HandleRequest(ctx, makeRequest(MethodMemoryUpdate, MemoryUpdateParams{ID: 2}))
	if res.Error != nil {
		t.Fatalf("no-op update must succeed: %+v", res.Error)
	}
	mustUnmarshal(t, res.Result, &updated)
	if updated.Anchor.Content != "session-scoped fact" {
		t.Fatalf("no-op update must keep content: %+v", updated.Anchor)
	}
}

func TestHandlerMemoryValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("sqlite validation skipped in -short")
	}
	_, h := newAnchorWiredHandler(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		method   string
		req      *JSONRPCRequest
		wantCode int
	}{
		{
			name:     "create without content",
			method:   MethodMemoryCreate,
			req:      makeRequest(MethodMemoryCreate, MemoryCreateParams{}),
			wantCode: ErrCodeInvalidParams,
		},
		{
			name:     "get without id",
			method:   MethodMemoryGet,
			req:      makeRequest(MethodMemoryGet, MemoryGetParams{ID: 0}),
			wantCode: ErrCodeInvalidParams,
		},
		{
			name:     "delete without id",
			method:   MethodMemoryDelete,
			req:      makeRequest(MethodMemoryDelete, MemoryDeleteParams{ID: 0}),
			wantCode: ErrCodeInvalidParams,
		},
		{
			name:     "update unknown id",
			method:   MethodMemoryUpdate,
			req:      makeRequest(MethodMemoryUpdate, MemoryUpdateParams{ID: 999}),
			wantCode: ErrCodeJobNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.HandleRequest(ctx, tc.req)
			if resp.Error == nil || resp.Error.Code != tc.wantCode {
				t.Fatalf("want code %d, got %+v", tc.wantCode, resp.Error)
			}
		})
	}
}

// newNoAnchorWiredHandlerForValidation builds a handler without an anchor
// store wired; the memory RPCs must respond cleanly, not panic.
func newNoAnchorWiredHandlerForValidation(t *testing.T) *Handler {
	t.Helper()
	st := newRealStore(t)
	mgr := NewSessionManager(st, &branchLLM{}, &branchToolsImpl{}, NewEmergencyState(nil), nil, nil, nil, st)
	return NewHandler(mgr, nil, nil, nil)
}

func TestHandlerMemoryWithoutAnchorStore(t *testing.T) {
	if testing.Short() {
		t.Skip("sqlite validation skipped in -short")
	}
	handler := newNoAnchorWiredHandlerForValidation(t)
	ctx := context.Background()
	resp := handler.HandleRequest(ctx, makeRequest(MethodMemoryList, MemoryListParams{}))
	if resp.Error == nil || resp.Error.Code != ErrCodeInternalError {
		t.Fatalf("unwired anchor store must yield a clean internal error, got %+v", resp.Error)
	}
}
