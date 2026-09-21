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

	// genCache memoizes GenerateEmbedding by exact input text — content-
	// addressed, so a changed text is automatically a cache miss with no
	// separate invalidation logic needed. Fixes a real redundant-work bug
	// found live (hojaDeRuta-embeddings-skills.md Fase 1): both
	// skill.Manager.Relevant and retrieval.Retriever.Search recomputed the
	// SAME text's embedding on every call, and Relevant in particular was
	// called once per agent tool-calling iteration within a single turn —
	// same userMessage, same skill descriptions, recomputed every time.
	// FIFO eviction (not true LRU) once genCacheCap is exceeded: simple,
	// and sufficient for the actual access pattern (bursty repeats of the
	// same handful of strings within one turn, not a long tail that needs
	// real recency tracking).
	genCache      map[string][]float32
	genCacheOrder []string
	genCacheCap   int

	// backend, when non-nil, makes GenerateEmbedding use a real model
	// (LlamaClient) instead of the bag-of-words hash — see
	// NewStoreWithBackend. Deliberately decided ONCE at construction and
	// never toggled per-call: mixing dimensions within one Store's
	// lifetime (hash embeddings are 384-dim, bge-m3 is 1024-dim) would make
	// CosineSimilarity silently return 0 for any hash-vs-real comparison
	// (mismatched lengths), which reads as "unrelated" instead of erroring
	// — a genuinely dangerous silent failure mode to allow. A backend
	// call failing after construction returns an error from
	// GenerateEmbedding for that call; it does NOT fall back to a
	// different-dimension hash embedding within the same Store.
	backend *LlamaClient
}

// defaultGenCacheCap bounds genCache's size. Generous relative to a
// realistic working set (one turn's query + a handful of enabled skill
// descriptions) without letting a long-running daemon's cache grow
// unbounded from one-off queries that never repeat.
const defaultGenCacheCap = 128

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

// NewStore creates a new in-memory embedding store using the deterministic
// bag-of-words hash (384-dim). This is the only constructor every existing
// caller uses — behavior is unchanged from before Fase 4 unless a caller
// explicitly opts into NewStoreWithBackend instead.
func NewStore(dsn string) (*Store, error) {
	// dsn ignored for in-memory; kept for API compatibility
	return &Store{
		entries:     make([]entry, 0),
		nextID:      1,
		dim:         384,
		genCache:    make(map[string][]float32),
		genCacheCap: defaultGenCacheCap,
	}, nil
}

// NewStoreWithBackend creates a Store that uses backend for every
// embedding, at dim dimensions (bge-m3 is 1024) — never the bag-of-words
// hash, for this Store's entire lifetime (see backend's doc comment on the
// Store struct for why mixing is unsafe). Callers are expected to have
// already health-checked backend (LlamaClient.Healthy) before calling
// this — NewStoreWithBackend itself does not probe the server, so a caller
// that skips the health check gets a Store whose every GenerateEmbedding
// call fails until the server is reachable, not a silent hash fallback.
func NewStoreWithBackend(backend *LlamaClient, dim int) (*Store, error) {
	if backend == nil {
		return nil, fmt.Errorf("NewStoreWithBackend: backend must not be nil (use NewStore for the hash fallback)")
	}
	if dim <= 0 {
		return nil, fmt.Errorf("NewStoreWithBackend: dim must be positive, got %d", dim)
	}
	return &Store{
		entries:     make([]entry, 0),
		nextID:      1,
		dim:         dim,
		genCache:    make(map[string][]float32),
		genCacheCap: defaultGenCacheCap,
		backend:     backend,
	}, nil
}

// Close closes the store (no-op for in-memory).
func (s *Store) Close() error {
	return nil
}

// GenCacheSize returns the number of distinct texts currently memoized by
// GenerateEmbedding. Exported for tests (in this package and callers like
// skill.Manager) that need to prove a burst of calls with repeated text
// produced one cache entry, not one recomputation per call — see genCache's
// doc comment on the Store struct.
func (s *Store) GenCacheSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.genCache)
}

// GenerateEmbedding generates a deterministic token-based bag-of-words embedding
// (hashing trick) for testing. It is a deterministic stand-in for a real model
// (production can swap to Ollama etc.), but unlike the previous whole-text
// hash it is token-overlap-aware: texts sharing vocabulary produce meaningfully
// positive cosine, while unrelated texts remain near zero.
// Tokenization: lowercased, split on non-alphanumeric runes, empty tokens skipped.
// Each token hashes to a bucket (h % dim) with signed hashing (+1/-1 from a bit
// of h) to zero-mean collision noise; the vector is then L2-normalized.
// Package-level single source of truth; Store.GenerateEmbedding delegates here.
func GenerateEmbedding(text string) ([]float32, error) {
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

// GenerateEmbedding returns text's embedding, memoized per exact input text
// (see genCache's doc comment on the Store struct). Uses the real backend
// (LlamaClient) when this Store was built via NewStoreWithBackend, or the
// deterministic bag-of-words hash otherwise — the package-level
// GenerateEmbedding. Never both within one Store's lifetime (see backend's
// doc comment on the Store struct): a backend call failing here returns
// the error, it does not silently fall back to a different-dimension hash.
func (s *Store) GenerateEmbedding(text string) ([]float32, error) {
	s.mu.RLock()
	if cached, ok := s.genCache[text]; ok {
		out := make([]float32, len(cached))
		copy(out, cached)
		s.mu.RUnlock()
		return out, nil
	}
	backend := s.backend
	s.mu.RUnlock()

	var emb []float32
	var err error
	if backend != nil {
		emb, err = backend.Generate(context.Background(), text)
	} else {
		emb, err = GenerateEmbedding(text)
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if _, exists := s.genCache[text]; !exists {
		if s.genCacheCap > 0 && len(s.genCacheOrder) >= s.genCacheCap {
			oldest := s.genCacheOrder[0]
			s.genCacheOrder = s.genCacheOrder[1:]
			delete(s.genCache, oldest)
		}
		cached := make([]float32, len(emb))
		copy(cached, emb)
		s.genCache[text] = cached
		s.genCacheOrder = append(s.genCacheOrder, text)
	}
	s.mu.Unlock()

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

// CosineSimilarity computes cosine similarity between two vectors.
// It is the exported single source of truth for BoW cosine (hashing trick, 384-dim).
// Returns 0 for mismatched lengths or zero vectors, otherwise dot/(|a|*|b|).
func CosineSimilarity(a, b []float32) float32 {
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

func cosineSimilarity(a, b []float32) float32 {
	return CosineSimilarity(a, b)
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// GenerateEmbeddingWithModel was a dead stub (no callers anywhere in the
// codebase) that silently ignored ctx and model and just called the hash.
// Removed — see llama.go's LlamaClient for the real backend
// (hojaDeRuta-embeddings-skills.md Fase 4) and NewStoreWithBackend for how
// a Store picks it up.
