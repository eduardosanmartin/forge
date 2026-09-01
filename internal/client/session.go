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

// MarkSuccess marks a session as human-verified successful.
func (c *Client) MarkSuccess(ctx context.Context, sessionID string) error {
	var result map[string]any
	return c.Call(ctx, daemon.MethodSessionMarkSuccess, daemon.SessionMarkSuccessParams{SessionID: sessionID}, &result)
}
