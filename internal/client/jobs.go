package client

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// ListJobs lists all background jobs via daemon RPC.
func (c *Client) ListJobs(ctx context.Context) (*daemon.JobListResult, error) {
	var result daemon.JobListResult
	if err := c.Call(ctx, daemon.MethodJobList, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetJob retrieves a single job by ID.
func (c *Client) GetJob(ctx context.Context, jobID string) (*daemon.JobResult, error) {
	var result daemon.JobResult
	if err := c.Call(ctx, daemon.MethodJobGet, daemon.JobGetParams{JobID: jobID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CancelJob cancels a running job by ID.
func (c *Client) CancelJob(ctx context.Context, jobID string) (*daemon.JobCancelResult, error) {
	var result daemon.JobCancelResult
	if err := c.Call(ctx, daemon.MethodJobCancel, daemon.JobCancelParams{JobID: jobID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}


