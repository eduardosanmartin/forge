package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeLlamaServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req llamaEmbeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Deterministic per-text fake vector: sum of byte values in the
		// first slot, everything else zero — enough to prove real request
		// content reached the server and a real response came back,
		// without needing an actual model.
		emb := make([]float32, dim)
		var sum float32
		for _, b := range []byte(req.Input) {
			sum += float32(b)
		}
		emb[0] = sum
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(llamaEmbeddingsResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{{Embedding: emb}},
		})
	}))
}

func TestLlamaClient_Generate_RealRequestRoundTrip(t *testing.T) {
	srv := fakeLlamaServer(t, 1024)
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "bge-m3")
	emb, err := c.Generate(t.Context(), "hola mundo")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(emb) != 1024 {
		t.Fatalf("embedding length = %d, want 1024", len(emb))
	}
	var wantSum float32
	for _, b := range []byte("hola mundo") {
		wantSum += float32(b)
	}
	if emb[0] != wantSum {
		t.Fatalf("emb[0] = %v, want %v (proves the real request text reached the server)", emb[0], wantSum)
	}
}

func TestLlamaClient_Generate_ServerErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "bge-m3")
	_, err := c.Generate(t.Context(), "anything")
	if err == nil {
		t.Fatal("expected an error from a 503 response, got nil")
	}
}

func TestLlamaClient_Generate_UnreachableServerErrors(t *testing.T) {
	// A port nothing is listening on — simulates the server not having
	// started yet, or having crashed.
	c := NewLlamaClient("http://127.0.0.1:1", "bge-m3")
	_, err := c.Generate(t.Context(), "anything")
	if err == nil {
		t.Fatal("expected a connection error, got nil")
	}
}

func TestLlamaClient_Healthy(t *testing.T) {
	srv := fakeLlamaServer(t, 1024)
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "bge-m3")
	if !c.Healthy(t.Context()) {
		t.Error("Healthy() should be true against a working fake server")
	}

	dead := NewLlamaClient("http://127.0.0.1:1", "bge-m3")
	if dead.Healthy(t.Context()) {
		t.Error("Healthy() should be false against an unreachable address")
	}
}

func TestLlamaClient_ProbeDimension(t *testing.T) {
	srv := fakeLlamaServer(t, 1024)
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "bge-m3")
	dim, err := c.ProbeDimension(t.Context())
	if err != nil {
		t.Fatalf("ProbeDimension: %v", err)
	}
	if dim != 1024 {
		t.Fatalf("dim = %d, want 1024 (measured from the fake server, not hardcoded)", dim)
	}

	dead := NewLlamaClient("http://127.0.0.1:1", "bge-m3")
	if _, err := dead.ProbeDimension(t.Context()); err == nil {
		t.Error("ProbeDimension against an unreachable server should error, not return a guessed dimension")
	}
}

func TestNewStoreWithBackend_UsesRealBackendNotHash(t *testing.T) {
	srv := fakeLlamaServer(t, 1024)
	defer srv.Close()

	backend := NewLlamaClient(srv.URL, "bge-m3")
	s, err := NewStoreWithBackend(backend, 1024)
	if err != nil {
		t.Fatalf("NewStoreWithBackend: %v", err)
	}
	defer s.Close()

	emb, err := s.GenerateEmbedding("hola mundo")
	if err != nil {
		t.Fatalf("GenerateEmbedding: %v", err)
	}
	if len(emb) != 1024 {
		t.Fatalf("embedding length = %d, want 1024 (from the fake backend, not the 384-dim hash)", len(emb))
	}
	var wantSum float32
	for _, b := range []byte("hola mundo") {
		wantSum += float32(b)
	}
	if emb[0] != wantSum {
		t.Fatal("Store.GenerateEmbedding did not route through the backend — got hash output instead")
	}
}

func TestNewStoreWithBackend_RejectsNilBackend(t *testing.T) {
	if _, err := NewStoreWithBackend(nil, 1024); err == nil {
		t.Fatal("expected an error for a nil backend")
	}
}

func TestNewStoreWithBackend_RejectsNonPositiveDim(t *testing.T) {
	srv := fakeLlamaServer(t, 1024)
	defer srv.Close()
	backend := NewLlamaClient(srv.URL, "bge-m3")
	if _, err := NewStoreWithBackend(backend, 0); err == nil {
		t.Fatal("expected an error for dim=0")
	}
}

// TestStore_GenerateEmbedding_BackendFailureErrorsNotHashFallback is the
// core safety property from backend's doc comment on the Store struct:
// once a Store is backend-mode, a failed call must return an error, never
// silently produce a 384-dim hash embedding that would then compare as
// "unrelated" (CosineSimilarity returns 0 on length mismatch) against
// every other embedding already cached at 1024-dim in this same Store.
func TestStore_GenerateEmbedding_BackendFailureErrorsNotHashFallback(t *testing.T) {
	backend := NewLlamaClient("http://127.0.0.1:1", "bge-m3") // unreachable
	s, err := NewStoreWithBackend(backend, 1024)
	if err != nil {
		t.Fatalf("NewStoreWithBackend: %v", err)
	}
	defer s.Close()

	_, err = s.GenerateEmbedding("anything")
	if err == nil {
		t.Fatal("expected an error when the backend is unreachable, got a (presumably hash-fallback) result instead")
	}
}
