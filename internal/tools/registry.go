// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"log/slog"
	"sync"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/routing"
)

// Registry holds all registered tools and coordinates execution with the permission engine.
type Registry struct {
	tools         map[string]Tool
	toolOrder     []string // maintains registration order
	permsEngine   *perms.Engine
	workspaceRoot string
	logger        *slog.Logger
	isolator      Isolator // OS-level shell isolation routing (RNF-4.7); nil = legacy direct exec
	requireIso    bool     // refuse shell_exec when isolation is required but unavailable (Linux only)
	mu            sync.RWMutex
	router        *routing.ModelRouter
}

// New creates a new Registry with the given permission engine and workspace root.
func New(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	r := &Registry{
		tools:         make(map[string]Tool),
		toolOrder:     make([]string, 0),
		permsEngine:   permsEngine,
		workspaceRoot: workspaceRoot,
		logger:        logger,
	}
	// Initialize router with default empty role models
	r.router = routing.NewModelRouter(map[routing.ModelRole]string{})
	return r
}

// SetIsolator routes shell children through the OS-isolation wrapper
// whenever the isolator reports Enabled. Safe to call before or after tool
// registration; the setting reaches the shell_exec tool either way. A nil
// isolator restores legacy direct execution.
func (r *Registry) SetIsolator(isol Isolator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.isolator = isol
	r.applyShellOptionsLocked()
}

// SetRequireShellIsolation configures whether shell_exec must refuse to run
// when isolation is unavailable. Only honored on Linux; other platforms
// ignore it (documented config behavior).
func (r *Registry) SetRequireShellIsolation(require bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requireIso = require
	r.applyShellOptionsLocked()
}

// applyShellOptionsLocked pushes registry-level shell configuration into the
// registered shell_exec tool. Caller holds r.mu for writing.
func (r *Registry) applyShellOptionsLocked() {
	if t, ok := r.tools["shell_exec"]; ok {
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
	if name == "shell_exec" {
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

// PermRequestSource lets a tool supply its own permission request instead of
// the name-based mapping in BuildPermsRequest. WU2 plugin tools implement
// this to map their declared manifest permission kind to a perms.Request;
// native tools continue using BuildPermsRequest unchanged.
type PermRequestSource interface {
	PermsRequest(args map[string]any) (perms.Request, error)
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

	// 2. Build perms.Request (plugin tools supply their own via PermRequestSource)
	var permsReq perms.Request
	var err error
	if src, ok := tool.(PermRequestSource); ok {
		permsReq, err = src.PermsRequest(args)
		if err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	} else {
		permsReq, err = BuildPermsRequest(name, args)
		if err != nil {
			return Result{Content: "ERROR: " + err.Error()}, nil
		}
	}

	// Args delivery channel for the v1 custom tools: Registry.Execute is
	// the only place that holds the raw, schema-validated arguments, so it
	// publishes them on req.Input instead of widening the Tool.Execute
	// signature for every tool. Base tools ignore Input (BuildPermsRequest
	// already filled their typed fields), so their behavior is unchanged;
	// req.Args remains the shell-only argv channel.
	permsReq.Input = args

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

// defaultRegistryTools returns the base tool set (the original five
// OS-reaching tools). Every entry is dependency-free.
func defaultRegistryTools(logger *slog.Logger) []Tool {
	return []Tool{
		newFsReadTool(),
		newFsWriteTool(),
		newFsListTool(),
		newShellExecTool(logger),
		newGitTool(),
	}
}

// v1RegistryTools returns the v1 feature tools. Each needs a real dependency
// (retriever, compactor, anchor store); registering them with nil deps is a
// nil-pointer landmine on first use, and no production call site constructs
// those deps yet — so they are registered only by NewDefaultRegistryWithDeps,
// the sole entry point that receives them.
func v1RegistryTools(
	retriever *retrieval.Retriever,
	compactor *compaction.Compactor,
	anchorStore *anchor.AnchorStoreSQL,
) []Tool {
	return []Tool{
		newRetrievalSearchTool(retriever),
		newCompactionSummarizeTool(compactor),
		newAnchoringStoreTool(anchorStore),
		newAnchoringListTool(anchorStore),
		newAnchoringGetTool(anchorStore),
		newAnchoringDeleteTool(anchorStore),
	}
}

// NewDefaultRegistry creates a registry with the base tools registered. The
// v1 feature tools are intentionally absent: they require real dependencies
// (see v1RegistryTools and NewDefaultRegistryWithDeps).
func NewDefaultRegistry(permsEngine *perms.Engine, workspaceRoot string, logger *slog.Logger) *Registry {
	r := New(permsEngine, workspaceRoot, logger)
	for _, tool := range defaultRegistryTools(logger) {
		r.Register(tool)
	}
	return r
}

// Unregister removes a tool by name. It is mutex-safe and idempotent: removing
// a non-existent tool is a no-op. WU2's plugin manager calls this on Disable.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; !exists {
		return
	}
	delete(r.tools, name)
	// Remove from toolOrder preserving order of remaining tools.
	for i, n := range r.toolOrder {
		if n == name {
			r.toolOrder = append(r.toolOrder[:i], r.toolOrder[i+1:]...)
			break
		}
	}
}

// NewDefaultRegistryWithDeps creates a registry with the base tools plus the
// v1 feature tools, wiring the real dependencies the v1 tools need.
func NewDefaultRegistryWithDeps(
	permsEngine *perms.Engine,
	workspaceRoot string,
	logger *slog.Logger,
	retriever *retrieval.Retriever,
	compactor *compaction.Compactor,
	anchorStore *anchor.AnchorStoreSQL,
) *Registry {
	r := New(permsEngine, workspaceRoot, logger)
	for _, tool := range defaultRegistryTools(logger) {
		r.Register(tool)
	}
	for _, tool := range v1RegistryTools(retriever, compactor, anchorStore) {
		r.Register(tool)
	}
	return r
}
