package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

// FormatToolArgs renders tool-call arguments as a compact "k=v,k2=v2"
// preview for single-line traces. Empty/null arguments render as ""; a value
// that is not valid JSON renders as "..."; output longer than 60 characters
// is elided.
func FormatToolArgs(args json.RawMessage) string {
	if len(args) == 0 || string(args) == "null" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(args, &decoded); err != nil {
		return "..."
	}
	var out string
	if obj, ok := decoded.(map[string]any); ok {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, obj[k]))
		}
		out = strings.Join(parts, ", ")
	} else {
		out = fmt.Sprintf("%v", decoded)
	}
	const maxLen = 60
	if len(out) > maxLen {
		out = out[:maxLen-3] + "..."
	}
	return out
}

// RunOptions tunes a one-shot execution.
type RunOptions struct {
	// SessionID reuses an existing session instead of creating an ephemeral
	// one. Empty means "create an ephemeral session tagged source=oneshot".
	SessionID string

	// Metadata is merged into the ephemeral session's metadata (ignored when
	// SessionID is set).
	Metadata map[string]any

	// EnableRetrieval enables selective context retrieval (v1).
	EnableRetrieval bool

	// EnableCompaction enables hierarchical conversation compaction (v1).
	EnableCompaction bool

	// EnableAnchoring enables persistent anchored facts (v1).
	EnableAnchoring bool

	// EnableRouting enables cost-based model routing per step (v1).
	EnableRouting bool
}

// UsageJSON carries token usage in wire-friendly JSON shape.
type UsageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolCallTrace records one tool call executed during the turn.
type ToolCallTrace struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	OK   bool            `json:"ok"`
}

// OneShotResult is the structured outcome of a single non-interactive turn.
type OneShotResult struct {
	SessionID  string          `json:"session_id"`
	Model      string          `json:"model"`
	Response   string          `json:"response"` // final assistant text
	ToolCalls  []ToolCallTrace `json:"tool_calls,omitempty"`
	Usage      *UsageJSON      `json:"usage,omitempty"`
	DurationMs int64           `json:"duration_ms"`
}

// RunOneShot creates (or reuses) a session, executes exactly one agent turn
// for prompt, and returns the structured result. The caller decides how to
// print it: human-readable text or indented JSON.
//
// The daemon's ExecuteTurn response carries the final assistant content, the
// model name, and a per-call tool trace, so no extra round-trips are needed.
func RunOneShot(ctx context.Context, cl *Client, prompt string, opts RunOptions) (*OneShotResult, error) {
	start := time.Now()

	sessionID := opts.SessionID
	if sessionID == "" {
		metadata := map[string]any{"source": "oneshot"}
		for k, v := range opts.Metadata {
			metadata[k] = v
		}
		var sess daemon.SessionResult
		if err := cl.Call(ctx, daemon.MethodCreateSession,
			daemon.CreateSessionParams{Metadata: metadata}, &sess); err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		sessionID = sess.ID
	}

	var res daemon.ExecuteTurnResult
	if err := cl.Call(ctx, daemon.MethodExecuteTurn,
		daemon.ExecuteTurnParams{
			SessionID:        sessionID,
			UserMessage:      prompt,
			EnableRetrieval:  opts.EnableRetrieval,
			EnableCompaction: opts.EnableCompaction,
			EnableAnchoring:  opts.EnableAnchoring,
			EnableRouting:    opts.EnableRouting,
		}, &res); err != nil {
		return nil, fmt.Errorf("execute turn: %w", err)
	}

	out := &OneShotResult{
		SessionID: sessionID,
		Model:     res.Model,
		Response:  res.FinalContent,
		// Sub-millisecond turns (in-memory mocks) still report 1ms so
		// consumers can rely on duration_ms > 0 for any completed turn.
		DurationMs: max(1, time.Since(start).Milliseconds()),
	}
	for _, tc := range res.ToolTrace {
		out.ToolCalls = append(out.ToolCalls, ToolCallTrace{
			Name: tc.Name,
			Args: tc.Args,
			OK:   tc.OK,
		})
	}
	if res.Usage != nil {
		out.Usage = &UsageJSON{
			PromptTokens:     res.Usage.PromptTokens,
			CompletionTokens: res.Usage.CompletionTokens,
			TotalTokens:      res.Usage.TotalTokens,
		}
	}
	return out, nil
}
