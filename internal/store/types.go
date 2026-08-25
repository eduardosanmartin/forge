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
	CreatedAt  int64
}

// Stats holds database statistics.
type Stats struct {
	SessionCount int64
	MessageCount int64
	DBSizeBytes  int64
}
