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
	toolOrder     []string // maintains registration order
	permsEngine   *perms.Engine
	workspaceRoot string
	logger        *slog.Logger
	isolator      Isolator // OS-level shell isolation routing (RNF-4.7); nil = legacy direct exec
	requireIso    bool     // refuse shell.exec when isolation is required but unavailable (Linux only)
	mu            sync.RWMutex
}

// New creates a new Registry with the given permission engine and workspace root.
func New(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	return &Registry{
		tools:         make(map[string]Tool),
		toolOrder:     make([]string, 0),
		permsEngine:   permsEngine,
		workspaceRoot: workspaceRoot,
		logger:        logger,
	}
}

// SetIsolator routes shell children through the OS-isolation wrapper
// whenever the isolator reports Enabled. Safe to call before or after tool
// registration; the setting reaches the shell.exec tool either way. A nil
// isolator restores legacy direct execution.
func (r *Registry) SetIsolator(isol Isolator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.isolator = isol
	r.applyShellOptionsLocked()
}

// SetRequireShellIsolation configures whether shell.exec must refuse to run
// when isolation is unavailable. Only honored on Linux; other platforms
// ignore it (documented config behavior).
func (r *Registry) SetRequireShellIsolation(require bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requireIso = require
	r.applyShellOptionsLocked()
}

// applyShellOptionsLocked pushes registry-level shell configuration into the
// registered shell.exec tool. Caller holds r.mu for writing.
func (r *Registry) applyShellOptionsLocked() {
	if t, ok := r.tools["shell.exec"]; ok {
		if se, ok := t.(*shellExecTool); ok {
			se.setOptions(r.logger, r.isolator, r.requireIso)
		}
	}
}

// Register adds a tool to the registry. Not thread-safe for concurrent registration;
// register all tools before concurrent Execute calls.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; !exists {
		r.toolOrder = append(r.toolOrder, name)
	}
	r.tools[name] = tool
	if name == "shell.exec" {
		// Late-registered shell tools still receive the configured routing.
		if se, ok := tool.(*shellExecTool); ok {
			se.setOptions(r.logger, r.isolator, r.requireIso)
		}
	}
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Tool, 0, len(r.toolOrder))
	for _, name := range r.toolOrder {
		if t, ok := r.tools[name]; ok {
			list = append(list, t)
		}
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
func defaultRegistryTools(logger *slog.Logger) []Tool {
	return []Tool{
		newFsReadTool(),
		newFsWriteTool(),
		newFsListTool(),
		newShellExecTool(logger),
		newGitTool(),
	}
}

// NewDefaultRegistry creates a registry with all standard tools registered.
func NewDefaultRegistry(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	r := New(permsEngine, workspaceRoot, logger)
	for _, tool := range defaultRegistryTools(logger) {
		r.Register(tool)
	}
	return r
}
