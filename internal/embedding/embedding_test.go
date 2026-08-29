package embedding

import (
	"testing"
)

func TestEmbeddingGenerateStoreSearch(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	// 1. Generate embedding
	text := "hola mundo"
	emb, err := store.GenerateEmbedding(text)
	if err != nil {
		t.Fatalf("GenerateEmbedding failed: %v", err)
	}
	if len(emb) == 0 {
		t.Fatal("embedding empty")
	}

	// 2. Store embedding
	id, err := store.Store(text, emb)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if id == 0 {
		t.Fatal("store returned zero id")
	}

	// 3. Search by similarity
	results, err := store.Search(text, 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no search results")
	}
	if results[0].ID != id {
		t.Fatalf("expected id %d, got %d", id, results[0].ID)
	}
	if results[0].Text != text {
		t.Fatalf("expected text %q, got %q", text, results[0].Text)
	}
	if results[0].Score <= 0.9 {
		t.Fatalf("expected high score for identical text, got %f", results[0].Score)
	}
}

func TestEmbeddingSearchDifferentTexts(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	texts := []string{
		"función para sumar dos números",
		"función que multiplica enteros",
		"cómo hacer un http request en go",
		"ejemplo de goroutine y channel",
	}
	ids := make([]int64, len(texts))
	for i, txt := range texts {
		emb, _ := store.GenerateEmbedding(txt)
		id, _ := store.Store(txt, emb)
		ids[i] = id
	}

	results, err := store.Search("sumar números", 3)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].ID != ids[0] {
		t.Logf("Warning: top result not exact match (id=%d vs %d)", results[0].ID, ids[0])
	}
}
