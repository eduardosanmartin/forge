package client

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// CreateSession creates a new session via the daemon.
func (c *Client) CreateSession(ctx context.Context, metadata map[string]any) (*daemon.SessionResult, error) {
	var result daemon.SessionResult
	if err := c.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: metadata}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Status returns daemon status.
func (c *Client) Status(ctx context.Context) (*daemon.StatusResult, error) {
	var result daemon.StatusResult
	if err := c.Call(ctx, daemon.MethodStatus, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ExecuteTurn executes a turn against a session.
func (c *Client) ExecuteTurn(ctx context.Context, sessionID, message string) (*daemon.ExecuteTurnResult, error) {
	var result daemon.ExecuteTurnResult
	if err := c.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: sessionID, UserMessage: message}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
