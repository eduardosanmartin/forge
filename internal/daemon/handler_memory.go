package daemon

import (
	"context"
	"encoding/json"

	"github.com/eduardosanmartin/forge/internal/anchor"
)

// defaultAnchorSession is the convention for user-created anchors that are
// not tied to a specific session: `forge memory add` without --session uses
// it so CLI-only facts still appear in list output and survive session
// deletion. Model-initiated anchoring always names a real session.
const defaultAnchorSession = "global"

// anchorResult converts a store anchor into its wire form.
func anchorResult(a anchor.Anchor) AnchorResult {
	return AnchorResult{
		ID:        a.ID,
		SessionID: a.SessionID,
		Content:   a.Content,
		Source:    a.Source,
		Tags:      a.Tags,
		CreatedAt: a.CreatedAt.UnixMilli(),
		UpdatedAt: a.UpdatedAt.UnixMilli(),
	}
}

// memoryUnavailable is the shared response when the anchor store is not wired.
func (h *Handler) memoryUnavailable(req *JSONRPCRequest) *JSONRPCResponse {
	return NewErrorResponse(req.ID, ErrCodeInternalError, "anchor store not configured", nil)
}

// RF-3.4 NOTE: the memory.* RPCs below are USER-initiated (they serve the
// `forge memory` CLI where the owner inspects/edits their own anchored
// facts). They intentionally do NOT pass through the permission engine or
// the RF-4.4 approval flow: those gates exist for MODEL-initiated anchoring
// via the anchoring tools. Owner transparency over their own memory is the
// point of RF-3.4 itself.

func (h *Handler) handleMemoryList(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	store := h.mgr.AnchorStore()
	if store == nil {
		return h.memoryUnavailable(req)
	}
	var params MemoryListParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		}
	}
	var anchors []anchor.Anchor
	var err error
	if params.SessionID != "" {
		anchors, err = store.List(ctx, params.SessionID)
	} else {
		anchors, err = store.ListAll(ctx)
	}
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "list anchors failed", err.Error())
	}
	result := MemoryListResult{Anchors: make([]AnchorResult, 0, len(anchors))}
	for _, a := range anchors {
		result.Anchors = append(result.Anchors, anchorResult(a))
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleMemoryGet(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	store := h.mgr.AnchorStore()
	if store == nil {
		return h.memoryUnavailable(req)
	}
	var params MemoryGetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.ID == 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "id is required", nil)
	}
	a, err := store.Get(ctx, params.ID)
	if err != nil {
		// Includes sql.ErrNoRows from the underlying query.
		return NewErrorResponse(req.ID, ErrCodeJobNotFound, "anchor not found", err.Error())
	}
	return h.resultResponse(req.ID, MemoryResult{Anchor: anchorResult(a)})
}

func (h *Handler) handleMemoryCreate(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	store := h.mgr.AnchorStore()
	if store == nil {
		return h.memoryUnavailable(req)
	}
	var params MemoryCreateParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Content == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "content is required", nil)
	}
	// Convention: session_id omitted -> "global" cross-session bucket
	// (documented in rpc.go MemoryCreateParams and the CLI flag help).
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = defaultAnchorSession
	}
	a, err := store.Create(ctx, anchor.Anchor{
		SessionID: sessionID,
		Content:   params.Content,
		Source:    params.Source,
		Tags:      params.Tags,
	})
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "create anchor failed", err.Error())
	}
	return h.resultResponse(req.ID, MemoryResult{Anchor: anchorResult(a)})
}

func (h *Handler) handleMemoryUpdate(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	store := h.mgr.AnchorStore()
	if store == nil {
		return h.memoryUnavailable(req)
	}
	var params MemoryUpdateParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.ID == 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "id is required", nil)
	}
	existing, err := store.Get(ctx, params.ID)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeJobNotFound, "anchor not found", err.Error())
	}
	// Patch semantics: pointer fields distinguish "no change" (nil) from
	// a real replacement, mirroring MemoryUpdateParams documentation.
	if params.Content != nil {
		existing.Content = *params.Content
	}
	if params.Source != nil {
		existing.Source = *params.Source
	}
	if params.Tags != nil {
		existing.Tags = *params.Tags
	}
	if err := store.Update(ctx, existing); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "update anchor failed", err.Error())
	}
	return h.resultResponse(req.ID, MemoryResult{Anchor: anchorResult(existing)})
}

func (h *Handler) handleMemoryDelete(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	store := h.mgr.AnchorStore()
	if store == nil {
		return h.memoryUnavailable(req)
	}
	var params MemoryDeleteParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.ID == 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "id is required", nil)
	}
	if err := store.Delete(ctx, params.ID); err != nil {
		return NewErrorResponse(req.ID, ErrCodeJobNotFound, "anchor not found", err.Error())
	}
	return h.resultResponse(req.ID, map[string]any{"deleted": true, "id": params.ID})
}
