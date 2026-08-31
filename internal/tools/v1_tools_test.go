// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
//
// This file proves the v1 custom tools work end-to-end through
// Registry.Execute once real dependencies are wired: arguments flow via
// perms.Request.Input, the tools hit a real anchor store / retriever /
// compactor, and base-tool behavior is unchanged in their presence.
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
)

// setupV1Registry builds a registry with the base five tools plus the six
// v1 feature tools wired to real, lightweight dependencies: an in-memory
// embedding store, a deterministic compactor, and an anchor store sharing
// a temp SQLite database created through the forge store itself (the same
// shape the daemon wiring produces).
func setupV1Registry(t *testing.T) (*Registry, string, *retrieval.Retriever, *anchor.AnchorStoreSQL) {
	t.Helper()
	tmpDir := t.TempDir()

	policy := perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"echo"}},
		Git:   perms.GitPermissions{Allow: []string{"status"}},
	}
	engine, err := perms.New(policy, tmpDir, slog.Default())
	if err != nil {
		t.Fatalf("create perms engine: %v", err)
	}

	embStore, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("create embedding store: %v", err)
	}
	retriever := retrieval.NewRetriever(embStore)
	compactor := compaction.NewCompactor(compaction.Config{})

	st, err := store.Open(filepath.Join(tmpDir, "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := anchor.CreateAnchorTable(context.Background(), st.DB()); err != nil {
		t.Fatalf("create anchors table: %v", err)
	}
	anchorStore := anchor.NewAnchorStoreSQL(st.DB())

	registry := NewDefaultRegistryWithDeps(engine, tmpDir, slog.Default(), retriever, compactor, anchorStore)
	return registry, tmpDir, retriever, anchorStore
}

// TestRegistryWithDeps_RegistersBaseAndV1Tools verifies the deps
// constructor registers the base five tools plus the six v1 tools, while
// NewDefaultRegistry keeps registering only the base five (covered by
// TestRegistry_List).
func TestRegistryWithDeps_RegistersBaseAndV1Tools(t *testing.T) {
	registry, _, _, _ := setupV1Registry(t)

	list := registry.List()
	if len(list) != 11 {
		t.Errorf("expected 11 tools (5 base + 6 v1), got %d", len(list))
	}
	expected := []string{
		"fs.read", "fs.write", "fs.list", "shell.exec", "git",
		"retrieval.search", "compaction.summarize",
		"anchoring.store", "anchoring.list", "anchoring.get", "anchoring.delete",
	}
	registered := make(map[string]bool, len(list))
	for _, tool := range list {
		registered[tool.Name()] = true
	}
	for _, name := range expected {
		if !registered[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

// TestRegistry_Execute_AnchoringStorePersistsAndReads proves the
// anchoring.store -> list -> get -> delete cycle persists through the real
// AnchorStoreSQL via the Input args channel.
func TestRegistry_Execute_AnchoringStorePersistsAndReads(t *testing.T) {
	registry, _, _, anchorStore := setupV1Registry(t)
	ctx := context.Background()

	// store
	res, err := registry.Execute(ctx, "anchoring.store", map[string]any{
		"content":    "The config schema version is 4",
		"session_id": "session-1",
		"tags":       []any{"config"},
	})
	if err != nil {
		t.Fatalf("anchoring.store: %v", err)
	}
	if !contains(res.Content, "Anchor created with ID") {
		t.Errorf("anchoring.store result: %s", res.Content)
	}

	// The anchor must exist in the real store, not just in the reply.
	anchors, err := anchorStore.List(ctx, "session-1")
	if err != nil {
		t.Fatalf("anchorStore.List: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("expected 1 persisted anchor, got %d", len(anchors))
	}
	if anchors[0].Content != "The config schema version is 4" {
		t.Errorf("persisted content = %q", anchors[0].Content)
	}
	if len(anchors[0].Tags) != 1 || anchors[0].Tags[0] != "config" {
		t.Errorf("persisted tags = %v", anchors[0].Tags)
	}
	id := anchors[0].ID

	// list
	res, err = registry.Execute(ctx, "anchoring.list", map[string]any{"session_id": "session-1"})
	if err != nil {
		t.Fatalf("anchoring.list: %v", err)
	}
	if !contains(res.Content, "The config schema version is 4") {
		t.Errorf("anchoring.list result missing content: %s", res.Content)
	}

	// get (JSON numbers arrive as float64, as they would from the model)
	res, err = registry.Execute(ctx, "anchoring.get", map[string]any{"id": float64(id)})
	if err != nil {
		t.Fatalf("anchoring.get: %v", err)
	}
	if !contains(res.Content, "The config schema version is 4") {
		t.Errorf("anchoring.get result missing content: %s", res.Content)
	}

	// delete
	res, err = registry.Execute(ctx, "anchoring.delete", map[string]any{"id": float64(id)})
	if err != nil {
		t.Fatalf("anchoring.delete: %v", err)
	}
	if !contains(res.Content, fmt.Sprintf("Anchor %d deleted", id)) {
		t.Errorf("anchoring.delete result: %s", res.Content)
	}

	// get after delete fails cleanly
	res, err = registry.Execute(ctx, "anchoring.get", map[string]any{"id": float64(id)})
	if err != nil {
		t.Fatalf("anchoring.get after delete: %v", err)
	}
	if !contains(res.Content, "ERROR:") {
		t.Errorf("expected clean ERROR for deleted anchor, got: %s", res.Content)
	}
}

// TestRegistry_Execute_RetrievalSearchReturnsTopChunk proves the search
// tool queries the real retriever. The v1 embedding is hash-based, so the
// only deterministic similarity guarantee is an exact-text match ranking
// first (cosine 1.0); semantic ranking arrives with a real embedding model.
func TestRegistry_Execute_RetrievalSearchReturnsTopChunk(t *testing.T) {
	registry, _, retriever, _ := setupV1Registry(t)
	ctx := context.Background()

	err := retriever.Index([]retrieval.Message{
		{ID: 1, Role: "user", Content: "the deploy pipeline uses terraform"},
		{ID: 2, Role: "assistant", Content: "the tests run with go test -race"},
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	res, err := registry.Execute(ctx, "retrieval.search", map[string]any{
		"query": "the tests run with go test -race",
		"k":     float64(2),
	})
	if err != nil {
		t.Fatalf("retrieval.search: %v", err)
	}
	if !contains(res.Content, `"message_id":2`) {
		t.Errorf("expected exact-match chunk (message_id 2) to rank first: %s", res.Content)
	}
	if !contains(res.Content, "the tests run with go test -race") {
		t.Errorf("expected chunk content in result: %s", res.Content)
	}
}

// TestRegistry_Execute_CompactionSummarizeCompactsProvidedTurns proves the
// summarize tool runs the real deterministic compactor over turns supplied
// through the Input channel.
func TestRegistry_Execute_CompactionSummarizeCompactsProvidedTurns(t *testing.T) {
	registry, _, _, _ := setupV1Registry(t)
	ctx := context.Background()

	turns := make([]any, 25)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		turns[i] = map[string]any{
			"role":    role,
			"content": fmt.Sprintf("turn %02d deterministic content", i),
		}
	}

	res, err := registry.Execute(ctx, "compaction.summarize", map[string]any{"turns": turns})
	if err != nil {
		t.Fatalf("compaction.summarize: %v", err)
	}
	if !contains(res.Content, `"OriginalTurns":25`) {
		t.Errorf("expected original turn count 25: %s", res.Content)
	}
	// 25 regular turns: 15 older summarized in one chunk + 10 kept verbatim.
	if !contains(res.Content, `"SummariesCreated":1`) {
		t.Errorf("expected 1 summary chunk: %s", res.Content)
	}
	if !contains(res.Content, "turn 00 deterministic content") {
		t.Errorf("expected older turn content inside the summary: %s", res.Content)
	}
}

// TestRegistry_Execute_CustomToolsCleanErrorsOnBadInput covers both the
// schema-validation layer (registry) and the nil-Input direct-call layer
// (tool.Execute invoked without the args channel, e.g. by tests).
func TestRegistry_Execute_CustomToolsCleanErrorsOnBadInput(t *testing.T) {
	registry, _, _, anchorStore := setupV1Registry(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		wantText string
	}{
		{
			name:     "missing required field caught by schema validation",
			tool:     "anchoring.store",
			args:     map[string]any{"content": "x"},
			wantText: "ERROR: schema validation: session_id: required field is missing",
		},
		{
			name:     "wrong type caught by schema validation",
			tool:     "retrieval.search",
			args:     map[string]any{"query": 42},
			wantText: "ERROR: schema validation: query: expected string, got int",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := registry.Execute(ctx, tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute(%s): %v", tc.tool, err)
			}
			if res.Content != tc.wantText {
				t.Errorf("Execute(%s) = %q, want %q", tc.tool, res.Content, tc.wantText)
			}
		})
	}

	// Direct tool.Execute without the Input channel (nil map) must
	// produce a clean tool-level error, never a panic.
	tools := []Tool{
		NewAnchoringStoreTool(anchorStore),
		NewAnchoringGetTool(anchorStore),
		NewRetrievalSearchTool(nil),
	}
	req := perms.Request{Kind: perms.KindCustom, Command: "probe"}
	for _, tool := range tools {
		res, err := tool.Execute(ctx, req)
		if err != nil {
			t.Fatalf("%s direct Execute: %v", tool.Name(), err)
		}
		if !contains(res.Content, "ERROR:") {
			t.Errorf("%s direct Execute should return clean ERROR, got: %s", tool.Name(), res.Content)
		}
	}
}

// TestRegistryWithDeps_BaseToolBehaviorUnchanged proves the base five
// tools keep their exact v0 behavior (schema validation, permissions,
// fencing) when the Input args channel and the v1 tools are present.
func TestRegistryWithDeps_BaseToolBehaviorUnchanged(t *testing.T) {
	registry, tmpDir, _, _ := setupV1Registry(t)
	ctx := context.Background()

	testFile := filepath.Join(tmpDir, "base.txt")
	if err := os.WriteFile(testFile, []byte("hello base"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	res, err := registry.Execute(ctx, "fs.read", map[string]any{"path": testFile})
	if err != nil {
		t.Fatalf("fs.read: %v", err)
	}
	if !contains(res.Content, "<<TOOL_RESULT:fs.read>>") {
		t.Errorf("result should be fenced: %s", res.Content)
	}
	if !contains(res.Content, "hello base") {
		t.Errorf("result should contain file content: %s", res.Content)
	}

	res, err = registry.Execute(ctx, "fs.read", map[string]any{})
	if err != nil {
		t.Fatalf("fs.read validation: %v", err)
	}
	if res.Content != "ERROR: schema validation: path: required field is missing" {
		t.Errorf("base schema validation changed: %s", res.Content)
	}
}
