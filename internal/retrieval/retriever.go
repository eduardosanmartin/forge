package retrieval

import (
	"errors"
	"strings"
	"sync"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

// Message represents a conversation message.
type Message struct {
	ID      int64
	Role    string
	Content string
}

// Chunk represents a retrievable chunk of text with metadata.
type Chunk struct {
	MessageID int64
	Role      string
	Content   string
	Score     float32
}

// Retriever handles indexing and searching conversation messages.
type Retriever struct {
	embStore *embedding.Store
	chunks   []Chunk
	mu       sync.RWMutex
}

// NewRetriever creates a new retriever with the given embedding store.
func NewRetriever(embStore *embedding.Store) *Retriever {
	return &Retriever{
		embStore: embStore,
		chunks:   make([]Chunk, 0),
	}
}

// Index indexes a slice of messages into the retriever.
// Each message is split into chunks (one per message for now).
func (r *Retriever) Index(messages []Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear existing chunks (re-index)
	r.chunks = r.chunks[:0]

	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}

		// Generate embedding for the message content
		emb, err := r.embStore.GenerateEmbedding(msg.Content)
		if err != nil {
			return err
		}

		// Store in embedding store
		_, err = r.embStore.Store(msg.Content, emb)
		if err != nil {
			return err
		}

		// Add to local chunks for metadata
		r.chunks = append(r.chunks, Chunk{
			MessageID: msg.ID,
			Role:      msg.Role,
			Content:   msg.Content,
		})
	}
	return nil
}

// Search searches for relevant chunks given a query.
func (r *Retriever) Search(query string, k int) ([]Chunk, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if k <= 0 {
		return nil, errors.New("k must be positive")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.chunks) == 0 {
		return nil, nil
	}

	// Use embedding store to search
	results, err := r.embStore.Search(query, k)
	if err != nil {
		return nil, err
	}

	// Map back to our chunks with metadata
	chunkMap := make(map[string]Chunk, len(r.chunks))
	for _, c := range r.chunks {
		chunkMap[c.Content] = c
	}

	var out []Chunk
	for _, res := range results {
		if chunk, ok := chunkMap[res.Text]; ok {
			chunk.Score = res.Score
			out = append(out, chunk)
		}
	}

	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Clear removes all indexed data.
func (r *Retriever) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = r.chunks[:0]
}

// Len returns the number of indexed chunks.
func (r *Retriever) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.chunks)
}
