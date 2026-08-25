// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"log/slog"
	"sync"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// Registry holds all registered tools and coordinates execution with the permission engine.
type Registry struct {
	tools         map[string]Tool
	permsEngine   *perms.Engine
	workspaceRoot string
	logger        *slog.Logger
	mu            sync.RWMutex
}

// New creates a new Registry with the given permission engine and workspace root.
func New(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	return &Registry{
		tools:         make(map[string]Tool),
		permsEngine:   permsEngine,
		workspaceRoot: workspaceRoot,
		logger:        logger,
	}
}

// Register adds a tool to the registry. Not thread-safe for concurrent registration;
// register all tools before concurrent Execute calls.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	return list
}

// Execute validates args, checks permissions, executes the tool, and applies fencing + redaction.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (Result, error) {
	tool, ok := r.Get(name)
	if !ok {
		return Result{Content: "ERROR: unknown tool " + name}, nil
	}

	// 1. Validate args against JSON schema
	if err := ValidateArgs(tool.JSONSchema(), args); err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}

	// 2. Build perms.Request
	permsReq, err := BuildPermsRequest(name, args)
	if err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}

	// 3. Check permissions
	decision := r.permsEngine.Check(permsReq)
	if !decision.Allowed {
		return Result{
			Content: "DENIED: " + decision.Rule,
			Metadata: map[string]any{
				"denied": true,
				"rule":   decision.Rule,
			},
		}, nil
	}

	// 4. Execute tool
	result, err := tool.Execute(ctx, permsReq)
	if err != nil {
		return Result{Content: "ERROR: " + err.Error()}, nil
	}

	// 5. Apply fencing + redaction
	fencedContent := RedactAndFence(name, result.Content)
	redactedMetadata := RedactMetadata(result.Metadata)

	return Result{
		Content:  fencedContent,
		Metadata: redactedMetadata,
	}, nil
}

// defaultRegistryTools returns the standard set of tools for the registry.
func defaultRegistryTools() []Tool {
	return []Tool{
		newFsReadTool(),
		newFsWriteTool(),
		newFsListTool(),
		newShellExecTool(),
		newGitTool(),
	}
}

// NewDefaultRegistry creates a registry with all standard tools registered.
func NewDefaultRegistry(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	r := New(permsEngine, workspaceRoot, logger)
	for _, tool := range defaultRegistryTools() {
		r.Register(tool)
	}
	return r
}
