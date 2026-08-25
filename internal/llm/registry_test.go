// Package llm implements forge's LLM provider abstraction with an
// OpenAI-compatible adapter (Ollama) and a model registry supporting hot-swap.
package llm

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/logging"
)

func TestRegistry_New_Basic(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-1", "model-2"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	// GetDefault should return the provider and first model
	provider, model := registry.GetDefault()
	if provider == nil {
		t.Fatal("GetDefault: provider is nil")
	}
	if model != "model-1" {
		t.Errorf("default model: got %q, want %q", model, "model-1")
	}
}

func TestRegistry_New_DefaultProviderNotFound(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "nonexistent",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	_, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err == nil {
		t.Fatal("expected error for missing default provider, got nil")
	}
	if !contains(err.Error(), "default_provider") {
		t.Errorf("error should mention default_provider: %v", err)
	}
}

func TestRegistry_New_AllowlistDenied(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{"different-host:9999"},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	_, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err == nil {
		t.Fatal("expected error for allowlist denial, got nil")
	}
	if !contains(err.Error(), "denied") && !contains(err.Error(), "allowlist") {
		t.Errorf("error should mention allowlist denial: %v", err)
	}
}

func TestRegistry_GetProvider(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-1"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
			"other": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-2"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	// Get existing provider
	provider, ok := registry.GetProvider("ollama")
	if !ok {
		t.Fatal("GetProvider(ollama): not found")
	}
	if provider == nil {
		t.Fatal("provider is nil")
	}

	// Get non-existing provider
	_, ok = registry.GetProvider("nonexistent")
	if ok {
		t.Error("GetProvider(nonexistent): should return false")
	}
}

func TestRegistry_SetDefault_HotSwapValid(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-a", "model-b", "model-c"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-a", "model-b", "model-c"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	// Initial default
	_, model := registry.GetDefault()
	if model != "model-a" {
		t.Errorf("initial model: got %q, want %q", model, "model-a")
	}

	// Hot-swap to model-b
	if err := registry.SetDefault("model-b"); err != nil {
		t.Fatalf("SetDefault(model-b): %v", err)
	}
	_, model = registry.GetDefault()
	if model != "model-b" {
		t.Errorf("after swap: got %q, want %q", model, "model-b")
	}

	// Hot-swap to model-c
	if err := registry.SetDefault("model-c"); err != nil {
		t.Fatalf("SetDefault(model-c): %v", err)
	}
	_, model = registry.GetDefault()
	if model != "model-c" {
		t.Errorf("after swap: got %q, want %q", model, "model-c")
	}
}

func TestRegistry_SetDefault_InvalidModel(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-a", "model-b"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-a", "model-b"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	// Try to set non-existent model
	err = registry.SetDefault("model-x")
	if err == nil {
		t.Fatal("expected error for invalid model, got nil")
	}
	if !contains(err.Error(), "model-x") || !contains(err.Error(), "not available") {
		t.Errorf("error should mention model not available: %v", err)
	}

	// Default should remain unchanged
	_, model := registry.GetDefault()
	if model != "model-a" {
		t.Errorf("default model changed unexpectedly: got %q", model)
	}
}

func TestRegistry_ListAll(t *testing.T) {
	mock1 := NewMockServer()
	defer mock1.Close()

	mock2 := NewMockServer()
	defer mock2.Close()

	mock1.SetDefaultResponse((&ModelsResponseBuilder{Models: []string{"model-a", "model-b"}}).Build())
	mock2.SetDefaultResponse((&ModelsResponseBuilder{Models: []string{"model-c"}}).Build())

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama1",
		Providers: map[string]config.Provider{
			"ollama1": {
				Kind:    "openai-compatible",
				BaseURL: mock1.URL(),
				Models:  []string{"model-a", "model-b"},
			},
			"ollama2": {
				Kind:    "openai-compatible",
				BaseURL: mock2.URL(),
				Models:  []string{"model-c"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock1.URL()), hostFromURL(mock2.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	// Wait for model fetches
	time.Sleep(200 * time.Millisecond)

	all := registry.ListAll()
	if len(all) != 3 {
		t.Errorf("ListAll count: got %d, want 3", len(all))
	}

	// Verify all models present with correct provider tags
	expected := map[string]string{
		"model-a": "ollama1",
		"model-b": "ollama1",
		"model-c": "ollama2",
	}
	for _, m := range all {
		provider, ok := expected[m.Name]
		if !ok {
			t.Errorf("unexpected model: %q", m.Name)
		}
		if m.Provider != provider {
			t.Errorf("model %q: provider got %q, want %q", m.Name, m.Provider, provider)
		}
		if m.Kind != "openai-compatible" {
			t.Errorf("model %q: kind got %q, want openai-compatible", m.Name, m.Kind)
		}
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-1", "model-2", "model-3"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1", "model-2", "model-3"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = registry.GetDefault()
				_, _ = registry.GetProvider("ollama")
				_ = registry.ListAll()
			}
		}()
	}

	// Concurrent writers (SetDefault)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := registry.SetDefault(model); err != nil {
					errors <- err
				}
				time.Sleep(time.Millisecond)
			}
		}([]string{"model-1", "model-2", "model-3"}[i%3])
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent error: %v", err)
	}
}

func TestRegistry_Chat_UsesDefaultProviderAndModel(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ChatResponseBuilder{ID: "test", Model: "model-1", Content: "response", FinishReason: "stop"}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	ctx, cancel := ContextWithTimeout(5 * time.Second)
	defer cancel()

	// Request without model should use default
	resp, err := registry.Chat(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Choices[0].Message.Content != "response" {
		t.Errorf("content: got %q, want %q", resp.Choices[0].Message.Content, "response")
	}

	// Verify request used default model
	req := mock.LastRequest()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if body["model"] != "model-1" {
		t.Errorf("request model: got %v, want model-1", body["model"])
	}
}

func TestRegistry_ChatStream_UsesDefaultProviderAndModel(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&StreamChunkBuilder{
			ID:    "test",
			Model: "model-1",
			Deltas: []StreamDelta{
				{Content: "Hello"},
				{Content: "", FinishReason: stringPtr("stop")},
			},
		}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	ctx, cancel := ContextWithTimeout(10 * time.Second)
	defer cancel()

	ch, err := registry.ChatStream(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	count := 0
	for range ch {
		count++
	}
	if count != 2 {
		t.Errorf("chunk count: got %d, want 2", count)
	}

	// Verify request used default model
	req := mock.LastRequest()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if body["model"] != "model-1" {
		t.Errorf("request model: got %v, want model-1", body["model"])
	}
	if body["stream"] != true {
		t.Errorf("stream: got %v, want true", body["stream"])
	}
}

func TestRegistry_Close_CleansUp(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"model-1"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := registry.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Operations after close should fail gracefully
	_, model := registry.GetDefault()
	if model != "" {
		t.Errorf("GetDefault after close: got %q, want empty", model)
	}
	_, ok := registry.GetProvider("ollama")
	if ok {
		t.Error("GetProvider after close: should return false")
	}
}

func TestRegistry_UnknownProviderKind(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "unknown-kind",
				BaseURL: mock.URL(),
				Models:  []string{"model-1"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	_, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err == nil {
		t.Fatal("expected error for unknown provider kind, got nil")
	}
	if !contains(err.Error(), "unknown provider kind") {
		t.Errorf("error should mention unknown kind: %v", err)
	}
}

func TestRegistry_EmptyConfig(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		Providers:     map[string]config.Provider{},
		Network: config.NetworkConfig{
			AllowedHosts: []string{},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	_, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err == nil {
		t.Fatal("expected error for empty providers, got nil")
	}
}

func TestRegistry_SingleProviderMultipleModels(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	mock.SetDefaultResponse(
		(&ModelsResponseBuilder{Models: []string{"small", "medium", "large"}}).Build(),
	)

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: mock.URL(),
				Models:  []string{"small", "medium", "large"},
			},
		},
		Network: config.NetworkConfig{
			AllowedHosts: []string{hostFromURL(mock.URL())},
		},
	}

	logger, _, _ := logging.New(logging.Config{Level: "error"})
	registry, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer registry.Close()

	time.Sleep(100 * time.Millisecond)

	all := registry.ListAll()
	if len(all) != 3 {
		t.Errorf("ListAll: got %d models, want 3", len(all))
	}
}
