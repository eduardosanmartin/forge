// Package retrieval implements per-session selective context retrieval
// (RF-3.2): past messages of a session are embedded once and the ones most
// similar to the current request are offered back to the model.
package retrieval

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

// Message is one persisted transcript message to index.
type Message struct {
	ID      int64
	Role    string
	Content string
}

// Chunk is one search hit.
type Chunk struct {
	MessageID int64
	Role      string
	Content   string
	Score     float32
}

// DefaultMaxSessions bounds how many sessions keep an in-memory index; the
// least recently used one is evicted beyond it (its next turn re-indexes
// from scratch, through the embedding cache).
const DefaultMaxSessions = 32

// Retriever keeps one independent index per session. It used to keep a
// single process-wide index that each turn rebuilt for whichever session
// ran last — so a turn in session A could be handed session B's history —
// while re-storing every message into the shared embedding.Store on every
// rebuild (O(n²) growth over a session, duplicate hits). Now each session's
// index only grows by the messages it hasn't seen yet.
type Retriever struct {
	embStore    *embedding.Store
	maxSessions int

	mu       sync.Mutex
	sessions map[string]*sessionIndex
	clock    uint64 // LRU tick
}

type sessionIndex struct {
	seen    map[int64]bool
	entries []indexed
	lastUse uint64
}

type indexed struct {
	chunk Chunk
	emb   []float32
}

// NewRetriever creates a retriever that embeds through embStore (its
// backend and generation cache); embStore's own entry list is not used.
func NewRetriever(embStore *embedding.Store) *Retriever {
	return &Retriever{
		embStore:    embStore,
		maxSessions: DefaultMaxSessions,
		sessions:    make(map[string]*sessionIndex),
	}
}

// IndexSession adds to sessionID's index every message it hasn't indexed
// yet (by message ID; ID 0 is always treated as new). Empty messages are
// skipped. Embeddings are generated outside the lock, so a slow embedding
// backend never blocks searches of other sessions.
func (r *Retriever) IndexSession(sessionID string, messages []Message) error {
	r.mu.Lock()
	idx := r.touch(sessionID)
	var todo []Message
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" || (m.ID != 0 && idx.seen[m.ID]) {
			continue
		}
		todo = append(todo, m)
	}
	r.mu.Unlock()

	fresh := make([]indexed, 0, len(todo))
	for _, m := range todo {
		emb, err := r.embStore.GenerateEmbedding(m.Content)
		if err != nil {
			return err
		}
		fresh = append(fresh, indexed{chunk: Chunk{MessageID: m.ID, Role: m.Role, Content: m.Content}, emb: emb})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	idx = r.touch(sessionID)
	for _, f := range fresh {
		if f.chunk.MessageID != 0 {
			if idx.seen[f.chunk.MessageID] {
				continue // indexed concurrently meanwhile
			}
			idx.seen[f.chunk.MessageID] = true
		}
		idx.entries = append(idx.entries, f)
	}
	return nil
}

// SearchSession returns up to k chunks of sessionID's index most similar to
// query, best first, skipping messages whose ID is in exclude (e.g. the
// ones already in the model's verbatim history window) and non-positive
// similarities.
func (r *Retriever) SearchSession(sessionID, query string, k int, exclude map[int64]bool) ([]Chunk, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if k <= 0 {
		return nil, errors.New("k must be positive")
	}
	r.mu.Lock()
	idx, ok := r.sessions[sessionID]
	var entries []indexed
	if ok {
		r.clock++
		idx.lastUse = r.clock
		entries = idx.entries // append-only: safe to read the prefix after unlock
	}
	r.mu.Unlock()
	if len(entries) == 0 {
		return nil, nil
	}

	q, err := r.embStore.GenerateEmbedding(query)
	if err != nil {
		return nil, err
	}
	hits := make([]Chunk, 0, len(entries))
	for _, e := range entries {
		if e.chunk.MessageID != 0 && exclude[e.chunk.MessageID] {
			continue
		}
		score := cosine(q, e.emb)
		if score <= 0 {
			continue
		}
		c := e.chunk
		c.Score = score
		hits = append(hits, c)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// Forget drops sessionID's index.
func (r *Retriever) Forget(sessionID string) {
	r.mu.Lock()
	delete(r.sessions, sessionID)
	r.mu.Unlock()
}

// Size reports how many entries sessionID's index holds.
func (r *Retriever) Size(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx, ok := r.sessions[sessionID]; ok {
		return len(idx.entries)
	}
	return 0
}

// touch returns sessionID's index, creating it (and evicting the least
// recently used one beyond maxSessions) as needed. Caller holds r.mu.
func (r *Retriever) touch(sessionID string) *sessionIndex {
	r.clock++
	idx, ok := r.sessions[sessionID]
	if !ok {
		if r.maxSessions > 0 && len(r.sessions) >= r.maxSessions {
			var oldestID string
			var oldest uint64 = math.MaxUint64
			for id, s := range r.sessions {
				if s.lastUse < oldest {
					oldest, oldestID = s.lastUse, id
				}
			}
			delete(r.sessions, oldestID)
		}
		idx = &sessionIndex{seen: make(map[int64]bool)}
		r.sessions[sessionID] = idx
	}
	idx.lastUse = r.clock
	return idx
}

func cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
