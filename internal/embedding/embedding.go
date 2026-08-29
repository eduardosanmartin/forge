package embedding

import (
	"context"
	"fmt"
	"math"
	"sync"
)

// Store manages embeddings in memory with brute-force cosine similarity.
// For production, swap to sqlite-vec or LanceDB.
type Store struct {
	mu      sync.RWMutex
	entries []entry
	nextID  int64
	dim     int
}

type entry struct {
	id        int64
	text      string
	embedding []float32
}

// SearchResult represents a similarity search result.
type SearchResult struct {
	ID    int64
	Text  string
	Score float32
}

// NewStore creates a new in-memory embedding store.
func NewStore(dsn string) (*Store, error) {
	// dsn ignored for in-memory; kept for API compatibility
	return &Store{
		entries: make([]entry, 0),
		nextID:  1,
		dim:     384,
	}, nil
}

// Close closes the store (no-op for in-memory).
func (s *Store) Close() error {
	return nil
}

// GenerateEmbedding generates a deterministic hash-based embedding for testing.
// In production, replace with real embedding model call (Ollama, etc.).
func (s *Store) GenerateEmbedding(text string) ([]float32, error) {
	const dim = 384
	emb := make([]float32, dim)

	hash := hashString(text)
	for i := 0; i < dim; i++ {
		hash = hash*1664525 + 1013904223
		emb[i] = float32(hash&0xFFFFFF)/0xFFFFFF*2.0 - 1.0
	}

	// Normalize to unit vector
	norm := float32(0)
	for _, v := range emb {
		norm += v * v
	}
	if norm > 0 {
		norm = float32(math.Sqrt(float64(norm)))
		for i := range emb {
			emb[i] /= norm
		}
	}

	return emb, nil
}

// Store saves text and its embedding, returns the row ID.
func (s *Store) Store(text string, embedding []float32) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(embedding) != s.dim {
		return 0, fmt.Errorf("embedding must be %d dimensions, got %d", s.dim, len(embedding))
	}

	// Copy embedding to avoid external mutation
	embCopy := make([]float32, s.dim)
	copy(embCopy, embedding)

	id := s.nextID
	s.nextID++
	s.entries = append(s.entries, entry{
		id:        id,
		text:      text,
		embedding: embCopy,
	})
	return id, nil
}

// Search finds similar texts by cosine similarity (brute force).
func (s *Store) Search(queryText string, k int) ([]SearchResult, error) {
	queryEmb, err := s.GenerateEmbedding(queryText)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	entries := make([]entry, len(s.entries))
	copy(entries, s.entries)
	s.mu.RUnlock()

	type scoredEntry struct {
		entry
		score float32
	}
	scored := make([]scoredEntry, 0, len(entries))
	for _, e := range entries {
		score := cosineSimilarity(queryEmb, e.embedding)
		scored = append(scored, scoredEntry{entry: e, score: score})
	}

	// Sort by score descending
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	if k > len(scored) {
		k = len(scored)
	}
	results := make([]SearchResult, k)
	for i := 0; i < k; i++ {
		results[i] = SearchResult{
			ID:    scored[i].id,
			Text:  scored[i].text,
			Score: scored[i].score,
		}
	}
	return results, nil
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// GenerateEmbeddingWithModel generates embedding using an external model (Ollama, etc.).
// Placeholder for production use with real embedding models.
func GenerateEmbeddingWithModel(ctx context.Context, model, text string) ([]float32, error) {
	store := &Store{}
	return store.GenerateEmbedding(text)
}
