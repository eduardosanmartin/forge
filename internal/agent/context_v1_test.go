// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
//
// This file proves the v1 feature flags produce real context behavior in
// ContextAssembler.Build once V1Deps are wired: anchoring injects anchors
// from the real anchor store, retrieval injects similar indexed chunks,
// and compaction swaps the history view for a deterministic summary plus
// recent verbatim turns — without deleting anything from the store.
package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// openTestAnchorStore creates a real AnchorStoreSQL on a temp SQLite
// database through the forge store (the same shape the daemon wiring
// produces).
func openTestAnchorStore(t *testing.T) *anchor.AnchorStoreSQL {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := anchor.CreateAnchorTable(context.Background(), st.DB()); err != nil {
		t.Fatalf("create anchors table: %v", err)
	}
	return anchor.NewAnchorStoreSQL(st.DB())
}

// newTestRetriever creates a retriever over the in-memory embedding store.
func newTestRetriever(t *testing.T) *retrieval.Retriever {
	t.Helper()
	embStore, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("create embedding store: %v", err)
	}
	return retrieval.NewRetriever(embStore)
}

// findSystemMessageByPrefix returns the first system message whose content
// starts with prefix.
func findSystemMessageByPrefix(messages []llm.Message, prefix string) (llm.Message, bool) {
	for _, m := range messages {
		if m.Role == "system" && len(m.Content) >= len(prefix) && m.Content[:len(prefix)] == prefix {
			return m, true
		}
	}
	return llm.Message{}, false
}

// countNonSystemMessages counts history/user messages (excludes the system
// prompt, tool definitions, and v1 injections).
func countNonSystemMessages(messages []llm.Message) int {
	count := 0
	for _, m := range messages {
		if m.Role != "system" {
			count++
		}
	}
	return count
}

// --- v1 anchoring ---

func TestContextAssembler_Build_AnchoringV1_InjectsStoredAnchors(t *testing.T) {
	ctx := context.Background()
	anchorStore := openTestAnchorStore(t)
	for _, a := range []anchor.Anchor{
		{SessionID: "session-1", Content: "The build tag must be forge_v1"},
		{SessionID: "session-1", Content: "Never edit the spec file"},
		{SessionID: "other-session", Content: "Anchor of another session"},
	} {
		if _, err := anchorStore.Create(ctx, a); err != nil {
			t.Fatalf("seed anchor: %v", err)
		}
	}

	assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
		session: &store.Session{
			ID:       "session-1",
			Metadata: map[string]any{"v1_anchoring": true},
		},
	}, 10)
	assembler.SetV1Deps(V1Deps{AnchorStore: anchorStore})

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	v1Msg, ok := findSystemMessageByPrefix(messages, "ANCHORED FACTS (v1):")
	if !ok {
		t.Fatal("expected an ANCHORED FACTS (v1): system message")
	}
	if !contains(v1Msg.Content, "The build tag must be forge_v1") ||
		!contains(v1Msg.Content, "Never edit the spec file") {
		t.Errorf("session anchors missing from injection: %s", v1Msg.Content)
	}
	if contains(v1Msg.Content, "Anchor of another session") {
		t.Errorf("anchors of other sessions must not leak: %s", v1Msg.Content)
	}
}

func TestContextAssembler_Build_AnchoringV1_DisabledCases(t *testing.T) {
	ctx := context.Background()
	anchorStore := openTestAnchorStore(t)
	if _, err := anchorStore.Create(ctx, anchor.Anchor{SessionID: "session-1", Content: "stored fact"}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}

	tests := []struct {
		name       string
		metadata   map[string]any
		anchorDep  *anchor.AnchorStoreSQL
		wantInject bool
	}{
		{
			name:      "flag off: no injection even with anchors and dep wired",
			metadata:  map[string]any{"v1_anchoring": false},
			anchorDep: anchorStore,
		},
		{
			name:      "dep nil: no injection even with flag on",
			metadata:  map[string]any{"v1_anchoring": true},
			anchorDep: nil,
		},
		{
			name:      "flag on, dep wired, but no anchors stored",
			metadata:  map[string]any{"v1_anchoring": true},
			anchorDep: openTestAnchorStore(t), // empty store
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
				session: &store.Session{ID: "session-1", Metadata: tc.metadata},
			}, 10)
			assembler.SetV1Deps(V1Deps{AnchorStore: tc.anchorDep})

			messages, err := assembler.Build(ctx, "session-1", "hello")
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if _, ok := findSystemMessageByPrefix(messages, "ANCHORED FACTS (v1):"); ok != tc.wantInject {
				t.Errorf("injection present = %v, want %v", ok, tc.wantInject)
			}
		})
	}
}

// TestContextAssembler_Build_AnchoringV1_V0FactsCoexist proves the v0
// anchored_facts metadata string keeps working alongside the v1 injection.
func TestContextAssembler_Build_AnchoringV1_V0FactsCoexist(t *testing.T) {
	ctx := context.Background()
	anchorStore := openTestAnchorStore(t)
	if _, err := anchorStore.Create(ctx, anchor.Anchor{SessionID: "session-1", Content: "v1 anchor content"}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}

	assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
		session: &store.Session{
			ID: "session-1",
			Metadata: map[string]any{
				"anchored_facts": "v0 fact string",
				"v1_anchoring":   true,
			},
		},
	}, 10)
	assembler.SetV1Deps(V1Deps{AnchorStore: anchorStore})

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, ok := findSystemMessageByPrefix(messages, "ANCHORED FACTS: v0 fact string"); !ok {
		t.Error("v0 anchored_facts message missing")
	}
	if _, ok := findSystemMessageByPrefix(messages, "ANCHORED FACTS (v1):"); !ok {
		t.Error("v1 anchors message missing")
	}
}

// --- v1 retrieval ---

// TestContextAssembler_Build_RetrievalV1_InjectsSimilarChunk proves the
// retrieval injection queries the real retriever. The v1 embedding is
// hash-based, so the deterministic guarantee is an exact-text match
// ranking first (cosine 1.0).
func TestContextAssembler_Build_RetrievalV1_InjectsSimilarChunk(t *testing.T) {
	ctx := context.Background()
	retriever := newTestRetriever(t)
	if err := retriever.Index([]retrieval.Message{
		{ID: 1, Role: "user", Content: "the deploy pipeline uses terraform"},
		{ID: 2, Role: "assistant", Content: "the tests run with go test -race"},
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
		session: &store.Session{
			ID:       "session-1",
			Metadata: map[string]any{"v1_retrieval": true},
		},
	}, 10)
	assembler.SetV1Deps(V1Deps{Retriever: retriever})

	messages, err := assembler.Build(ctx, "session-1", "the tests run with go test -race")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	retrievalMsg, ok := findSystemMessageByPrefix(messages, "RELEVANT CONTEXT (v1):")
	if !ok {
		t.Fatal("expected a RELEVANT CONTEXT (v1): system message")
	}
	// The exact-match chunk must rank first; with k=3 and only two indexed
	// chunks the unrelated one may also appear, but strictly below.
	exactIdx := strings.Index(retrievalMsg.Content, "[assistant] the tests run with go test -race")
	otherIdx := strings.Index(retrievalMsg.Content, "the deploy pipeline uses terraform")
	if exactIdx < 0 {
		t.Fatalf("expected the exact-match chunk in the injection: %s", retrievalMsg.Content)
	}
	if otherIdx >= 0 && otherIdx < exactIdx {
		t.Errorf("unrelated chunk ranked above the exact match: %s", retrievalMsg.Content)
	}
}

func TestContextAssembler_Build_RetrievalV1_DisabledCases(t *testing.T) {
	ctx := context.Background()
	indexedRetriever := newTestRetriever(t)
	if err := indexedRetriever.Index([]retrieval.Message{
		{ID: 1, Role: "user", Content: "indexed content"},
	}); err != nil {
		t.Fatalf("index: %v", err)
	}
	emptyRetriever := newTestRetriever(t)

	tests := []struct {
		name       string
		metadata   map[string]any
		retriever  *retrieval.Retriever
		userMsg    string
		wantInject bool
	}{
		{
			name:      "flag off: no injection",
			metadata:  map[string]any{"v1_retrieval": false},
			retriever: indexedRetriever,
			userMsg:   "indexed content",
		},
		{
			name:      "flag on but empty index: no injection",
			metadata:  map[string]any{"v1_retrieval": true},
			retriever: emptyRetriever,
			userMsg:   "indexed content",
		},
		{
			name:      "flag on but continuation iteration (empty user message): no injection",
			metadata:  map[string]any{"v1_retrieval": true},
			retriever: indexedRetriever,
			userMsg:   "",
		},
		{
			name:      "flag on but dep nil: no injection",
			metadata:  map[string]any{"v1_retrieval": true},
			retriever: nil,
			userMsg:   "indexed content",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
				session: &store.Session{ID: "session-1", Metadata: tc.metadata},
			}, 10)
			assembler.SetV1Deps(V1Deps{Retriever: tc.retriever})

			messages, err := assembler.Build(ctx, "session-1", tc.userMsg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if _, ok := findSystemMessageByPrefix(messages, "RELEVANT CONTEXT (v1):"); ok != tc.wantInject {
				t.Errorf("injection present = %v, want %v", ok, tc.wantInject)
			}
		})
	}
}

// --- v1 compaction ---

// seedCompactionSession creates a real store session with the compaction
// flag on and appends filler messages plus the current user message,
// mirroring the production flow (the user message is persisted before
// Build runs). Returns the session ID and the total message count.
func seedCompactionSession(t *testing.T, st *store.Store, fillers int) string {
	t.Helper()
	ctx := context.Background()
	session, err := st.CreateSession(ctx, map[string]any{"v1_compaction": true})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < fillers; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, _, err := st.AppendMessage(ctx, &store.Message{
			SessionID: session.ID,
			Role:      role,
			Content:   fmt.Sprintf("filler turn %02d deterministic content", i),
		}); err != nil {
			t.Fatalf("append filler %d: %v", i, err)
		}
	}
	if _, _, err := st.AppendMessage(ctx, &store.Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "current question about the fillers",
	}); err != nil {
		t.Fatalf("append current user message: %v", err)
	}
	return session.ID
}

func TestContextAssembler_Build_CompactionV1_AboveThresholdCompactsView(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// 44 fillers + current user message = 45 persisted messages (> 40).
	sessionID := seedCompactionSession(t, st, 44)

	assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
	assembler.SetV1Deps(V1Deps{Compactor: compaction.NewCompactor(compaction.Config{})})

	messages, err := assembler.Build(ctx, sessionID, "current question about the fillers")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The compacted view carries one deterministic summary system message
	// covering the older turns...
	summaryMsg, ok := findSystemMessageByPrefix(messages, "COMPACTED HISTORY (v1):")
	if !ok {
		t.Fatal("expected a COMPACTED HISTORY (v1): system message above the threshold")
	}
	if !contains(summaryMsg.Content, "filler turn 00 deterministic content") {
		t.Errorf("oldest turns should be covered by the summary: %s", summaryMsg.Content)
	}

	// ...plus the most recent turns verbatim (sliding window of 20
	// messages + the current user message).
	if got := countNonSystemMessages(messages); got != 21 {
		t.Errorf("non-system message count = %d, want 21 (20 verbatim + current user)", got)
	}
	// The newest filler stays verbatim inside the window (the two trailing
	// messages are the current user message, duplicated by existing v0
	// behavior, so scan the whole tail).
	tail := messages[len(messages)-22:]
	found := false
	for _, m := range tail {
		if contains(m.Content, "filler turn 43 deterministic content") {
			found = true
			break
		}
	}
	if !found {
		t.Error("newest filler should stay verbatim inside the recent window")
	}

	// Non-destructive: the store keeps every persisted row.
	transcript, err := st.GetMessagesSince(ctx, sessionID, 0)
	if err != nil {
		t.Fatalf("GetMessagesSince: %v", err)
	}
	if len(transcript) != 45 {
		t.Errorf("store rows = %d, want 45 (compaction must never delete)", len(transcript))
	}
}

func TestContextAssembler_Build_CompactionV1_AtOrBelowThresholdKeepsWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// 39 fillers + current user message = 40 persisted messages (not
	// above the threshold of 40): the view must stay the plain window.
	sessionID := seedCompactionSession(t, st, 39)

	assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
	assembler.SetV1Deps(V1Deps{Compactor: compaction.NewCompactor(compaction.Config{})})

	messages, err := assembler.Build(ctx, sessionID, "current question about the fillers")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, ok := findSystemMessageByPrefix(messages, "COMPACTED HISTORY (v1):"); ok {
		t.Error("no compaction expected at or below the threshold")
	}
	if got := countNonSystemMessages(messages); got != 21 {
		t.Errorf("non-system message count = %d, want 21 (plain window)", got)
	}
}

func TestContextAssembler_Build_CompactionV1_NilDepOrFlagOffNoCompaction(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	sessionID := seedCompactionSession(t, st, 44) // above threshold

	tests := []struct {
		name      string
		metadata  map[string]any
		compactor *compaction.Compactor
	}{
		{
			name:     "flag on but dep nil",
			metadata: map[string]any{"v1_compaction": true},
		},
		{
			name:      "flag off with dep wired",
			metadata:  map[string]any{"v1_compaction": false},
			compactor: compaction.NewCompactor(compaction.Config{}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session, err := st.GetSession(ctx, sessionID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			session.Metadata = tc.metadata
			if err := st.UpdateSessionMetadata(ctx, sessionID, tc.metadata); err != nil {
				t.Fatalf("update metadata: %v", err)
			}

			assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
			assembler.SetV1Deps(V1Deps{Compactor: tc.compactor})

			messages, err := assembler.Build(ctx, sessionID, "current question about the fillers")
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if _, ok := findSystemMessageByPrefix(messages, "COMPACTED HISTORY (v1):"); ok {
				t.Error("no compaction expected without flag AND dep both active")
			}
		})
	}
}

// --- v1 placeholders are gone ---

// TestContextAssembler_Build_NoPlaceholderAnnouncements proves the v1
// flags no longer emit placeholder announcements: with all four flags on,
// Build contains none of the bracketed placeholders, and routing (which
// acts on model selection in the agent loop, not on context) adds nothing.
func TestContextAssembler_Build_NoPlaceholderAnnouncements(t *testing.T) {
	ctx := context.Background()
	assembler := NewContextAssembler(tools.New(nil, "", nil), &contextMockStore{
		session: &store.Session{
			ID: "session-1",
			Metadata: map[string]any{
				"v1_retrieval":  true,
				"v1_compaction": true,
				"v1_anchoring":  true,
				"v1_routing":    true,
			},
		},
	}, 10)
	// No deps wired: flags on with nil deps inject nothing (nil-safe).

	messages, err := assembler.Build(ctx, "session-1", "hello")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, placeholder := range []string{
		"[RETRIEVAL ENABLED]",
		"[COMPACTION ENABLED]",
		"[ROUTING ENABLED]",
	} {
		for _, m := range messages {
			if contains(m.Content, placeholder) {
				t.Errorf("placeholder %q still present: %s", placeholder, m.Content)
			}
		}
	}
}
