package embedding

import (
	"context"
	"fmt"
	"math"
	"strings"
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

// GenerateEmbedding generates a deterministic token-based bag-of-words embedding
// (hashing trick) for testing. It is a deterministic stand-in for a real model
// (production can swap to Ollama etc.), but unlike the previous whole-text
// hash it is token-overlap-aware: texts sharing vocabulary produce meaningfully
// positive cosine, while unrelated texts remain near zero.
// Tokenization: lowercased, split on non-alphanumeric runes, empty tokens skipped.
// Each token hashes to a bucket (h % dim) with signed hashing (+1/-1 from a bit
// of h) to zero-mean collision noise; the vector is then L2-normalized.
func (s *Store) GenerateEmbedding(text string) ([]float32, error) {
	const dim = 384
	emb := make([]float32, dim)

	// Tokenize: lowercase + split on non-alphanumeric.
	lower := strings.ToLower(text)
	var tokens []string
	var cur strings.Builder
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}

	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		h := hashString(tok)
		bucket := int(h % uint32(dim))
		// Signed hashing: use bit 0 of h for sign.
		sign := float32(1)
		if (h & 1) == 0 {
			sign = -1
		}
		emb[bucket] += sign
	}

	// If no tokens (empty or only symbols), return zero vector (cosine will be 0).
	// Otherwise L2-normalize to unit vector.
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
