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

	if err := r.Index(messages); err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	// Search - verify infrastructure works (returns results, respects K)
	results, err := r.Search("suma", 3)
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

	results, err := r.Search("", 5)
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

	results, err := r.Search("anything", 5)
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
	r.Index(messages)

	for k := 1; k <= 5; k++ {
		results, _ := r.Search("test", k)
		if len(results) > k {
			t.Errorf("k=%d: got %d results, expected <= %d", k, len(results), k)
		}
	}
}

func TestRetrieverClear(t *testing.T) {
	embStore, _ := embedding.NewStore(":memory:")
	r := NewRetriever(embStore)

	messages := []Message{{ID: 1, Role: "user", Content: "test"}}
	r.Index(messages)
	if r.Len() != 1 {
		t.Errorf("expected 1 chunk after index, got %d", r.Len())
	}

	r.Clear()
	if r.Len() != 0 {
		t.Errorf("expected 0 chunks after clear, got %d", r.Len())
	}

	results, _ := r.Search("test", 5)
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
	r.Index([]Message{{ID: 1, Role: "user", Content: "algo indexado para que Search tenga contra qué buscar"}})

	const query = "same query repeated across every iteration of one turn"
	const simulatedIterations = 5
	for i := 0; i < simulatedIterations; i++ {
		if _, err := r.Search(query, 5); err != nil {
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
