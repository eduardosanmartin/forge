// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

// Request represents a tool invocation request from the agent.
type Request struct {
	Name string
	Args map[string]any
}

// Result represents a tool execution result returned to the agent.
// Content is the PRIMARY string returned to the model.
// Metadata carries structured extras (line counts, exit codes, etc.) for potential programmatic use.
type Result struct {
	Content  string
	Metadata map[string]any
}
