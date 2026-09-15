package llm

import (
	"context"
	"net/http"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/logging"
)

// chainTestSetup builds a registry with one mock-backed provider per name in
// providerModels (each provider's declared model is providerModels[name][0])
// and the given fallback_chain, defaultProvider "default" (must be a key of
// providerModels). Returns the registry and each provider's mock server,
// keyed by name, so a test can SetHandler("/v1/chat/completions", ...) on
// any of them to control that provider's Chat behavior.
func chainTestSetup(t *testing.T, defaultProvider string, providerModels map[string][]string, fallbackChain []string) (*Registry, map[string]*MockServer) {
	t.Helper()
	mocks := make(map[string]*MockServer, len(providerModels))
	providers := make(map[string]config.Provider, len(providerModels))
	var allowedHosts []string
	for name, models := range providerModels {
		mock := NewMockServer()
		t.Cleanup(mock.Close)
		mock.SetHandler("/v1/models", fixedHandler((&ModelsResponseBuilder{Models: models}).Build()))
		mocks[name] = mock
		providers[name] = config.Provider{Kind: "openai-compatible", BaseURL: mock.URL(), Models: models}
		allowedHosts = append(allowedHosts, hostFromURL(mock.URL()))
	}

	cfg := &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: defaultProvider,
		Providers:       providers,
		FallbackChain:   fallbackChain,
		Network:         config.NetworkConfig{AllowedHosts: allowedHosts},
	}
	logger, _, _ := logging.New(logging.Config{Level: "error"})
	reg, err := New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg, mocks
}

// fixedHandler always returns resp, ignoring the request — a convenience
// for SetHandler when a provider's behavior doesn't need to vary per call.
func fixedHandler(resp *MockResponse) MockHandler {
	return func(*http.Request) *MockResponse { return resp }
}

func TestChatWithFallback_FirstSucceedsNoFallback(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "p1", Content: "from primary", FinishReason: "stop"}).Build()))
	mocks["backup"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "b1", Content: "from backup", FinishReason: "stop"}).Build()))

	resp, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatWithFallback: %v", err)
	}
	if resp.Choices[0].Message.Content != "from primary" {
		t.Errorf("content = %q, want \"from primary\" (backup must not be called at all)", resp.Choices[0].Message.Content)
	}
	if chatRequestCount(mocks["backup"]) != 0 {
		t.Errorf("backup got %d requests, want 0 (first attempt succeeded)", chatRequestCount(mocks["backup"]))
	}
}

func TestChatWithFallback_RetryableFailureFallsToSecond(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))
	mocks["backup"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "b1", Content: "from backup", FinishReason: "stop"}).Build()))

	resp, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatWithFallback: %v", err)
	}
	if resp.Choices[0].Message.Content != "from backup" {
		t.Errorf("content = %q, want \"from backup\"", resp.Choices[0].Message.Content)
	}
}

func TestChatWithFallback_TwoFailuresFallsToThird(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup1": {"b1"}, "backup2": {"c1"}},
		[]string{"backup1/b1", "backup2/c1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))
	mocks["backup1"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusServiceUnavailable, Message: "down"}).Build()))
	mocks["backup2"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "c1", Content: "from backup2", FinishReason: "stop"}).Build()))

	resp, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatWithFallback: %v", err)
	}
	if resp.Choices[0].Message.Content != "from backup2" {
		t.Errorf("content = %q, want \"from backup2\"", resp.Choices[0].Message.Content)
	}
}

func TestChatWithFallback_AllFailReportsEveryAttempt(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "primary rate limited"}).Build()))
	mocks["backup"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusServiceUnavailable, Message: "backup down"}).Build()))

	_, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error when every model in the chain fails")
	}
	msg := err.Error()
	for _, want := range []string{"primary/p1", "backup/b1", "429", "503"} {
		if !containsSubstr(msg, want) {
			t.Errorf("error = %q, want it to mention %q (every attempt should be traceable)", msg, want)
		}
	}
}

func TestChatWithFallback_NonRetryableStopsImmediately(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusBadRequest, Message: "malformed request"}).Build()))
	mocks["backup"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "b1", Content: "from backup", FinishReason: "stop"}).Build()))

	_, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected the 400 to fail the call")
	}
	if chatRequestCount(mocks["backup"]) != 0 {
		t.Errorf("backup got %d requests, want 0 (400 is not retryable — must not try the chain)", chatRequestCount(mocks["backup"]))
	}
}

func TestChatWithFallback_NoChainConfiguredBehavesLikePlainFailure(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}}, nil)
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))

	_, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error — no fallback_chain configured means a retryable failure still fails the call")
	}
}

func TestChatWithFallback_MidChainNonRetryableStopsBeforeLastEntry(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup1": {"b1"}, "backup2": {"c1"}},
		[]string{"backup1/b1", "backup2/c1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))
	mocks["backup1"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusUnauthorized, Message: "bad key"}).Build()))
	mocks["backup2"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "c1", Content: "from backup2", FinishReason: "stop"}).Build()))

	_, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an error: backup1's 401 is not retryable, so the chain must stop there")
	}
	if chatRequestCount(mocks["backup2"]) != 0 {
		t.Errorf("backup2 got %d requests, want 0 (chain must stop at the non-retryable 401)", chatRequestCount(mocks["backup2"]))
	}
}

// chatRequestCount counts only /v1/chat/completions hits on mock, ignoring
// the /v1/models refresh every provider gets once at registry construction
// (present on every mock regardless of whether ChatWithFallback ever reaches
// that provider — counting ALL requests would false-positive here).
func chatRequestCount(mock *MockServer) int {
	n := 0
	for _, r := range mock.Requests() {
		if r.Path == "/v1/chat/completions" {
			n++
		}
	}
	return n
}

func containsSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
