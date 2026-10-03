package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// llmSummaries wires a Hierarchy over the store's SQLite with a fake
// summarizer that counts its calls.
func llmSummaries(t *testing.T, st *store.Store, calls *atomic.Int64) *compaction.Hierarchy {
	t.Helper()
	if err := compaction.CreateSummaryTable(context.Background(), st.DB()); err != nil {
		t.Fatal(err)
	}
	c := compaction.NewCompactor(compaction.Config{})
	return compaction.NewHierarchy(compaction.NewSQLSummaryCache(st.DB()),
		func(_ context.Context, prompt string) (string, error) {
			return fmt.Sprintf("LLM SUMMARY %d", calls.Add(1)), nil
		}, "small", 4, c.SummarizeBlock)
}

// RF-3.3: summaries precomputed between turns replace the deterministic
// ones in the compacted view, and Build itself never calls the model.
func TestBuildUsesPrecomputedLLMSummaries(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sessionID := seedCompactionSession(t, st, 80)

	var calls atomic.Int64
	assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
	assembler.SetV1Deps(V1Deps{
		Compactor: compaction.NewCompactor(compaction.Config{}),
		Summaries: llmSummaries(t, st, &calls),
	})

	// Before any precompute: deterministic fallback.
	messages, err := assembler.Build(ctx, sessionID, "current question about the fillers")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("Build called the summary model %d times", calls.Load())
	}
	first, ok := findSystemMessageByPrefix(messages, "COMPACTED HISTORY (v1):")
	if !ok || !strings.Contains(first.Content, "RESUMEN:") {
		t.Fatalf("want the deterministic summary before precompute, got %+v", first)
	}

	if err := assembler.PrecomputeSummaries(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Fatal("precompute generated no summaries")
	}
	before := calls.Load()
	messages, err = assembler.Build(ctx, sessionID, "current question about the fillers")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("Build called the summary model")
	}
	var compacted []string
	for _, m := range messages {
		if strings.HasPrefix(m.Content, "COMPACTED HISTORY (v1):") {
			compacted = append(compacted, m.Content)
		}
	}
	if len(compacted) == 0 {
		t.Fatal("no compacted history")
	}
	for _, c := range compacted {
		if !strings.Contains(c, "LLM SUMMARY") {
			t.Errorf("compacted entry not from the summary model: %q", c)
		}
	}
}

// Precompute summarizes closed blocks beyond the current compacted region
// too, so they are ready when the window moves past them.
func TestPrecomputeCoversBlocksAheadOfTheWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sessionID := seedCompactionSession(t, st, 80)

	var calls atomic.Int64
	h := llmSummaries(t, st, &calls)
	assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
	assembler.SetV1Deps(V1Deps{Compactor: compaction.NewCompactor(compaction.Config{}), Summaries: h})
	if err := assembler.PrecomputeSummaries(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	viewBefore := compactedView(t, assembler, sessionID)
	generated := calls.Load()

	// Grow the session by 30 messages: the window moves forward, and the
	// newly compacted blocks were already summarized.
	for i := 0; i < 30; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, _, err := st.AppendMessage(ctx, &store.Message{SessionID: sessionID, Role: role, Content: fmt.Sprintf("later %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	// The view changes (the window moved; with fan-in 4 the closed blocks
	// may even collapse into a level-2 summary)...
	viewAfter := compactedView(t, assembler, sessionID)
	if fmt.Sprint(viewAfter) == fmt.Sprint(viewBefore) {
		t.Fatal("the window did not move past precomputed blocks")
	}
	// ...and none of it is generated during Build.
	if calls.Load() != generated {
		t.Fatal("Build generated summaries")
	}
	messages, err := assembler.Build(ctx, sessionID, "later 29")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if strings.HasPrefix(m.Content, "COMPACTED HISTORY (v1):") && strings.Contains(m.Content, "RESUMEN:") {
			// Only blocks closed after the precompute may still fall back.
			if strings.Contains(m.Content, "filler turn") {
				t.Errorf("block closed before precompute still uses the fallback: %q", m.Content)
			}
		}
	}
}

func TestPrecomputeSkipsSessionsWithoutCompaction(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := st.CreateSession(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		_, _, _ = st.AppendMessage(ctx, &store.Message{SessionID: session.ID, Role: "user", Content: "x"})
	}
	var calls atomic.Int64
	assembler := NewContextAssembler(tools.New(nil, "", nil), st, 10)
	assembler.SetV1Deps(V1Deps{Compactor: compaction.NewCompactor(compaction.Config{}), Summaries: llmSummaries(t, st, &calls)})
	if err := assembler.PrecomputeSummaries(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("summarized a session with compaction off (%d calls)", calls.Load())
	}
}

// The agent queues a session for background precompute after each turn.
func TestAgentTurnSchedulesBackgroundSummaries(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sessionID := seedCompactionSession(t, st, 80)

	var calls atomic.Int64
	llmReg := &mockLLMRegistry{provider: newMockProvider(func(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
		return llm.ChatResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "ok"}}}}, nil
	})}
	permsEng := newTestPermsEngine(t)
	a := NewAgent(config.Defaults(), st, llmReg, tools.NewDefaultRegistry(permsEng, "", nil), permsEng, newTestLogger())
	bgCtx, stopBg := context.WithCancel(ctx)
	defer stopBg() // before the store closes (defers run in reverse)
	a.SetV1Deps(V1Deps{Compactor: compaction.NewCompactor(compaction.Config{}), Summaries: llmSummaries(t, st, &calls), BackgroundCtx: bgCtx})

	if _, err := a.ExecuteTurn(ctx, sessionID, "next"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no background summaries after the turn")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// compactedView returns the compacted-history entries Build renders.
func compactedView(t *testing.T, a *ContextAssembler, sessionID string) []string {
	t.Helper()
	msgs, err := a.Build(context.Background(), sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "COMPACTED HISTORY (v1):") {
			out = append(out, m.Content)
		}
	}
	return out
}
