// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// Tool is the MCP-shaped interface that every native tool implements.
type Tool interface {
	// Name returns the tool's unique identifier (e.g., "fs.read").
	Name() string
	// Description returns a human and model-readable description of the tool.
	Description() string
	// JSONSchema returns a JSON Schema (draft-07 compatible) for the tool's arguments.
	JSONSchema() map[string]any
	// Execute runs the tool with the given request. The Request carries the Kind
	// and relevant fields for permission checking.
	Execute(ctx context.Context, req perms.Request) (Result, error)
}
