// Package store implements SQLite-backed session and message persistence for forge.
package store

import (
	"github.com/eduardosanmartin/forge/internal/llm"
)

// Session represents a conversation session.
type Session struct {
	ID        string
	CreatedAt int64
	UpdatedAt int64
	Metadata  map[string]any // JSON-decoded
}

// Message represents a single message in a session's conversation log.
type Message struct {
	ID         int64
	SessionID  string
	Seq        int
	Role       string
	Content    string
	ToolCalls  []llm.ToolCall // decoded from JSON
	ToolCallID string
	Name       string
	Usage      *llm.Usage
	Model      string // model that produced this message (assistant only)
	DurationMs int64  // wall-clock time of the LLM call that produced this message (assistant only)
	CreatedAt  int64
}

// SessionCompare holds a side-by-side comparison of two sessions.
type SessionCompare struct {
	SessionA       Session   `json:"session_a"`
	SessionB       Session   `json:"session_b"`
	BranchAtSeqA   int       `json:"branch_at_seq_a"`
	BranchAtSeqB   int       `json:"branch_at_seq_b"`
	BranchParentA  string    `json:"branch_parent_a"`
	BranchParentB  string    `json:"branch_parent_b"`
	BranchRootA    string    `json:"branch_root_a"`
	BranchRootB    string    `json:"branch_root_b"`
	CountA         int       `json:"count_a"`
	CountB         int       `json:"count_b"`
	DivergentA     []Message `json:"divergent_a"`
	DivergentB     []Message `json:"divergent_b"`
	DivergentCountA int      `json:"divergent_count_a"`
	DivergentCountB int      `json:"divergent_count_b"`
	SameSession    bool      `json:"same_session"`
}

// Stats holds database statistics.
type Stats struct {
	SessionCount int64
	MessageCount int64
	DBSizeBytes  int64
}
