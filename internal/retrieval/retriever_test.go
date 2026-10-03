package retrieval

import (
	"testing"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

func TestRetrieverIndexAndSearch(t *testing.T) {
	embStore, err := embedding.NewStore(":memory:")
	if err != nil {
		t.Fatalf("embedding.NewStore: %v", err)
	}
	defer embStore.Close()

	r := NewRetriever(embStore)

	messages := []Message{
		{ID: 1, Role: "user", Content: "Quiero crear una función que sume dos números en Go"},
		{ID: 2, Role: "assistant", Content: "Aquí tienes una función suma: func sum(a, b int) int { return a + b }"},
		{ID: 3, Role: "user", Content: "Ahora necesito una que multiplique"},
		{ID: 4, Role: "assistant", Content: "Función multiplicar: func mul(a, b int) int { return a * b }"},
		{ID: 5, Role: "user", Content: "¿Cómo hago un HTTP GET request en Go?"},
		{ID: 6, Role: "assistant", Content: "Usa http.Get: resp, err := http.Get(url)"},
	}

	if err := r.IndexSession("s", messages); err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	// Search - verify infrastructure works (returns results, respects K)
	results, err := r.SearchSession("s", "suma", 3, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results returned - retrieval infrastructure not working")
	}
	if len(results) > 3 {
		t.Errorf("expected <= 3 results, got %d", len(results))
	}
	// Verify result structure
	for _, res := range results {
		if res.MessageID == 0 {
			t.Error("result missing MessageID")
		}
		if res.Content == "" {
			t.Error("result missing Content")
		}
		if res.Score < 0 || res.Score > 1 {
			t.Errorf("invalid score %f", res.Score)
		}
	}
}

func TestRetrieverEmptyQuery(t *testing.T) {
	embStore, _ := embedding.NewStore(":memory:")
	r := NewRetriever(embStore)

	results, err := r.SearchSession("s", "", 5, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("empty query should return no results, got %d", len(results))
	}
}

func TestRetrieverNoIndexedData(t *testing.T) {
	embStore, _ := embedding.NewStore(":memory:")
	r := NewRetriever(embStore)

	results, err := r.SearchSession("s", "anything", 5, nil)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("no indexed data should return no results, got %d", len(results))
	}
}

func TestRetrieverRespectsK(t *testing.T) {
	embStore, _ := embedding.NewStore(":memory:")
	r := NewRetriever(embStore)

	messages := []Message{
		{ID: 1, Role: "user", Content: "uno"},
		{ID: 2, Role: "user", Content: "dos"},
		{ID: 3, Role: "user", Content: "tres"},
		{ID: 4, Role: "user", Content: "cuatro"},
		{ID: 5, Role: "user", Content: "cinco"},
	}
	r.IndexSession("s", messages)

	for k := 1; k <= 5; k++ {
		results, _ := r.SearchSession("s", "test", k, nil)
		if len(results) > k {
			t.Errorf("k=%d: got %d results, expected <= %d", k, len(results), k)
		}
	}
}

func TestRetrieverClear(t *testing.T) {
	embStore, _ := embedding.NewStore(":memory:")
	r := NewRetriever(embStore)

	messages := []Message{{ID: 1, Role: "user", Content: "test"}}
	r.IndexSession("s", messages)
	if r.Size("s") != 1 {
		t.Errorf("expected 1 chunk after index, got %d", r.Size("s"))
	}

	r.Forget("s")
	if r.Size("s") != 0 {
		t.Errorf("expected 0 chunks after clear, got %d", r.Size("s"))
	}

	results, _ := r.SearchSession("s", "test", 5, nil)
	if len(results) != 0 {
		t.Errorf("clear should remove all data, got %d results", len(results))
	}
}

// TestRetriever_Search_CachesQueryAcrossRepeatedCalls is the retrieval-side
// half of the Fase 1 regression (hojaDeRuta-embeddings-skills.md): Search
// is called once per agent tool-calling iteration within a single turn
// (same shared root cause as skill.Manager.Relevant — internal/agent/
// context.go's Build calls it every iteration with the same userMessage).
// The embedding.Store this Retriever wraps now memoizes GenerateEmbedding
// by exact text, so repeated Search calls with the same query must not
// grow the cache past 1 entry for the query, regardless of call count.
func TestRetriever_Search_CachesQueryAcrossRepeatedCalls(t *testing.T) {
	embStore, err := embedding.NewStore(":memory:")
	if err != nil {
		t.Fatalf("embedding.NewStore: %v", err)
	}
	defer embStore.Close()
	r := NewRetriever(embStore)
	// Search short-circuits to a no-op before ever touching the embedding
	// store when there is nothing indexed yet — index one message so the
	// real Search -> embStore.Search -> GenerateEmbedding path actually runs.
	r.IndexSession("s", []Message{{ID: 1, Role: "user", Content: "algo indexado para que Search tenga contra qué buscar"}})

	const query = "same query repeated across every iteration of one turn"
	const simulatedIterations = 5
	for i := 0; i < simulatedIterations; i++ {
		if _, err := r.SearchSession("s", query, 5, nil); err != nil {
			t.Fatalf("Search call %d: %v", i, err)
		}
	}

	// 1 indexed message + 1 query = 2 distinct texts, regardless of
	// simulatedIterations.
	if got := embStore.GenCacheSize(); got != 2 {
		t.Fatalf("embed cache size after %d repeated Search() calls with the same query = %d, want 2 (1 indexed message + 1 query)",
			simulatedIterations, got)
	}
}

// C2 regression (2026-10-02 review): one process-wide index rebuilt per
// turn meant a turn in session A could search session B's history.
func TestRetrieverSessionsAreIsolated(t *testing.T) {
	embStore, _ := embedding.NewStore("")
	r := NewRetriever(embStore)
	if err := r.IndexSession("A", []Message{{ID: 1, Role: "user", Content: "alpha secret plan"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.IndexSession("B", []Message{{ID: 2, Role: "user", Content: "bravo other topic"}}); err != nil {
		t.Fatal(err)
	}
	hits, _ := r.SearchSession("A", "bravo other topic", 5, nil)
	for _, h := range hits {
		if h.MessageID == 2 {
			t.Fatal("session A's search returned session B's message")
		}
	}
}

// C2 regression: re-indexing the whole transcript every turn used to
// re-store every message (O(n^2) growth, duplicate hits).
func TestRetrieverReindexIsIncremental(t *testing.T) {
	embStore, _ := embedding.NewStore("")
	r := NewRetriever(embStore)
	var transcript []Message
	for turn := 1; turn <= 50; turn++ {
		transcript = append(transcript, Message{ID: int64(turn), Role: "user", Content: "message number " + string(rune('a'+turn%26))})
		if err := r.IndexSession("s", transcript); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Size("s"); got != 50 {
		t.Fatalf("index holds %d entries after 50 incremental turns, want 50", got)
	}
	hits, _ := r.SearchSession("s", "message number b", 10, nil)
	seen := map[int64]bool{}
	for _, h := range hits {
		if seen[h.MessageID] {
			t.Fatalf("duplicate hit for message %d", h.MessageID)
		}
		seen[h.MessageID] = true
	}
}

func TestRetrieverExcludeSkipsWindowMessages(t *testing.T) {
	embStore, _ := embedding.NewStore("")
	r := NewRetriever(embStore)
	_ = r.IndexSession("s", []Message{{ID: 1, Role: "user", Content: "exact text"}, {ID: 2, Role: "user", Content: "exact text too"}})
	hits, _ := r.SearchSession("s", "exact text", 5, map[int64]bool{1: true})
	for _, h := range hits {
		if h.MessageID == 1 {
			t.Fatal("excluded message 1 (already in the verbatim window) was returned")
		}
	}
}

func TestRetrieverEvictsLeastRecentlyUsedSession(t *testing.T) {
	embStore, _ := embedding.NewStore("")
	r := NewRetriever(embStore)
	r.maxSessions = 2
	_ = r.IndexSession("old", []Message{{ID: 1, Role: "user", Content: "x"}})
	_ = r.IndexSession("mid", []Message{{ID: 2, Role: "user", Content: "y"}})
	_, _ = r.SearchSession("old", "x", 1, nil) // "old" is now more recent than "mid"
	_ = r.IndexSession("new", []Message{{ID: 3, Role: "user", Content: "z"}})
	if r.Size("mid") != 0 || r.Size("old") != 1 || r.Size("new") != 1 {
		t.Fatalf("sizes old=%d mid=%d new=%d, want mid evicted", r.Size("old"), r.Size("mid"), r.Size("new"))
	}
}

// N2: after the embedding backend changes dimension, every index must be
// rebuilt (old vectors can't be compared with new ones).
func TestRetrieverResetAll(t *testing.T) {
	embStore, _ := embedding.NewStore("")
	r := NewRetriever(embStore)
	_ = r.IndexSession("a", []Message{{ID: 1, Role: "user", Content: "x"}})
	_ = r.IndexSession("b", []Message{{ID: 2, Role: "user", Content: "y"}})
	r.ResetAll()
	if r.Size("a") != 0 || r.Size("b") != 0 {
		t.Fatal("ResetAll must empty every session index")
	}
	_ = r.IndexSession("a", []Message{{ID: 1, Role: "user", Content: "x"}})
	if r.Size("a") != 1 {
		t.Fatal("a message seen before the reset must be indexed again")
	}
}
