package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/repomap"
)

// CodeSymbolsTool searches the workspace's top-level symbols (F5 repo
// map): a cheap first step before fs_read instead of exploring with
// fs_list. Read-only over the workspace's own code.
type CodeSymbolsTool struct {
	index *repomap.Index
}

// NewCodeSymbolsTool wraps a repo map index.
func NewCodeSymbolsTool(ix *repomap.Index) *CodeSymbolsTool { return &CodeSymbolsTool{index: ix} }

func (t *CodeSymbolsTool) Name() string { return "code_symbols" }

func (t *CodeSymbolsTool) Description() string {
	return "Find where a type, function or method is declared in this workspace: returns path:line, kind and signature. Use it before fs_read to locate code."
}

func (t *CodeSymbolsTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Symbol name or part of it (case-insensitive)"},
		},
		"required": []string{"query"},
	}
}

// PermsRequest: reading workspace code is gated like fs.read of the
// indexed root. The path is the index's own root, not ".", which would
// resolve against the process cwd instead of the workspace.
func (t *CodeSymbolsTool) PermsRequest(args map[string]any) (perms.Request, error) {
	return perms.Request{Kind: perms.KindFsRead, Path: t.index.Root(), Input: args}, nil
}

func (t *CodeSymbolsTool) Execute(_ context.Context, req perms.Request) (Result, error) {
	q, _ := req.Input["query"].(string)
	if strings.TrimSpace(q) == "" {
		return Result{Content: "ERROR: query is required"}, nil
	}
	matches := t.index.Search(q, 30)
	if len(matches) == 0 {
		return Result{Content: fmt.Sprintf("no symbol matching %q", q)}, nil
	}
	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "%s:%d  %s  %s\n", m.Path, m.Line, m.Kind, m.Signature)
	}
	return Result{Content: sb.String(), Metadata: map[string]any{"matches": len(matches)}}, nil
}
