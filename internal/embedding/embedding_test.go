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
		t.Fatalf("expected top result to be 'sumar números' text (id=%d vs %d) scores: %+v", ids[0], results[0].ID, results)
	}
	// Also ensure unrelated scores are near zero and shared-vocab scores are positive.
	s, _ := NewStore("")
	ea, _ := s.GenerateEmbedding("función para sumar dos números")
	eb, _ := s.GenerateEmbedding("sumar números")
	ec, _ := s.GenerateEmbedding("ejemplo de goroutine y channel")
	dotAB := float32(0)
	for i := range ea {
		dotAB += ea[i] * eb[i]
	}
	dotAC := float32(0)
	for i := range ea {
		dotAC += ea[i] * ec[i]
	}
	if dotAB <= 0.3 {
		t.Fatalf("expected shared vocabulary to score >0.3, got %f", dotAB)
	}
	if dotAC > 0.2 {
		t.Fatalf("expected unrelated pair to score <0.2, got %f", dotAC)
	}
}

func TestEmbeddingIdenticalVsUnrelated(t *testing.T) {
	s, _ := NewStore("")
	// Identical texts must be 1.0
	a := "Provides guidance for code reviews and pull request style checks"
	ea, _ := s.GenerateEmbedding(a)
	eb, _ := s.GenerateEmbedding(a)
	dot := float32(0)
	for i := range ea {
		dot += ea[i] * eb[i]
	}
	if dot < 0.99 {
		t.Fatalf("identical texts should score ~1.0, got %f", dot)
	}
	// Unrelated texts (no shared content words) should be near 0
	ec, _ := s.GenerateEmbedding("weather forecast gardening cooking recipes")
	dot2 := float32(0)
	for i := range ea {
		dot2 += ea[i] * ec[i]
	}
	if dot2 > 0.2 {
		t.Fatalf("unrelated texts should score <0.2, got %f", dot2)
	}
	// Paraphrase sharing several content words should be clearly above unrelated
	ed, _ := s.GenerateEmbedding("Please review this code for style issues and PR feedback")
	dot3 := float32(0)
	for i := range ea {
		dot3 += ea[i] * ed[i]
	}
	if dot3 <= dot2+0.15 {
		t.Fatalf("paraphrase sharing words should score clearly above unrelated: paraphrase %f vs unrelated %f", dot3, dot2)
	}
	if dot3 < 0.3 {
		t.Fatalf("paraphrase should score >=0.3, got %f", dot3)
	}
}
