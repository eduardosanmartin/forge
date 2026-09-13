package client

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// ListSessions lists sessions via daemon RPC.
func (c *Client) ListSessions(ctx context.Context, limit, offset int) (*daemon.ListSessionsResult, error) {
	var result daemon.ListSessionsResult
	if err := c.Call(ctx, daemon.MethodListSessions, daemon.ListSessionsParams{Limit: limit, Offset: offset}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetMessages retrieves messages for a session.
func (c *Client) GetMessages(ctx context.Context, sessionID string, limit, offset int) (*daemon.GetMessagesResult, error) {
	var result daemon.GetMessagesResult
	if err := c.Call(ctx, daemon.MethodGetMessages, daemon.GetMessagesParams{SessionID: sessionID, Limit: limit, Offset: offset}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetMessagesSince retrieves messages since seq.
func (c *Client) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error) {
	var result daemon.GetMessagesResult
	if err := c.Call(ctx, daemon.MethodGetMessagesSince, daemon.GetMessagesSinceParams{SessionID: sessionID, SinceSeq: sinceSeq}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetSession retrieves a single session.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*daemon.SessionResult, error) {
	var result daemon.SessionResult
	if err := c.Call(ctx, daemon.MethodGetSession, daemon.GetSessionParams{SessionID: sessionID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// BranchSession creates a branch from sourceID at atSeq.
func (c *Client) BranchSession(ctx context.Context, sourceID string, atSeq int, metadata map[string]any) (*daemon.SessionResult, error) {
	var result daemon.SessionResult
	if err := c.Call(ctx, daemon.MethodBranchSession, daemon.BranchSessionParams{SourceSessionID: sourceID, AtSeq: atSeq, Metadata: metadata}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// MergeSession appends source tail onto target.
func (c *Client) MergeSession(ctx context.Context, sourceID, targetID string) (*daemon.SessionResult, error) {
	var result daemon.SessionResult
	if err := c.Call(ctx, daemon.MethodMergeSession, daemon.MergeSessionParams{SourceSessionID: sourceID, TargetSessionID: targetID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// MarkSuccess marks a session as human-verified successful.
func (c *Client) MarkSuccess(ctx context.Context, sessionID string) error {
	var result map[string]any
	return c.Call(ctx, daemon.MethodSessionMarkSuccess, daemon.SessionMarkSuccessParams{SessionID: sessionID}, &result)
}

// HaltSession halts a running turn for a session.
func (c *Client) HaltSession(ctx context.Context, sessionID, reason string) error {
	var result map[string]any
	return c.Call(ctx, daemon.MethodHaltSession, daemon.HaltSessionParams{SessionID: sessionID, Reason: reason}, &result)
}

// ResumeSession resumes a halted session.
func (c *Client) ResumeSession(ctx context.Context, sessionID string) error {
	var result map[string]any
	return c.Call(ctx, daemon.MethodResumeSession, daemon.ResumeSessionParams{SessionID: sessionID}, &result)
}

// SwitchModel switches the session's model.
func (c *Client) SwitchModel(ctx context.Context, sessionID, model string) error {
	var result map[string]any
	return c.Call(ctx, daemon.MethodSwitchModel, daemon.SwitchModelParams{SessionID: sessionID, Model: model}, &result)
}

// CompareSessions compares two sessions side-by-side.
func (c *Client) CompareSessions(ctx context.Context, aID, bID string) (*daemon.CompareSessionsResult, error) {
	var result daemon.CompareSessionsResult
	if err := c.Call(ctx, daemon.MethodCompareSessions, daemon.CompareSessionsParams{SessionA: aID, SessionB: bID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
