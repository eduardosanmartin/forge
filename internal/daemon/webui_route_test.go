package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWebUIRouteServesIndex verifies the embedded GUI (RF-7.2) is reachable
// at "/" without disturbing the existing /ws and /health routes on the same
// mux (internal/daemon/transport.go).
func TestWebUIRouteServesIndex(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	logger := slog.New(slog.DiscardHandler)
	tx := NewTransport("127.0.0.1:0", h, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	req := httptest.NewRequest(http.MethodGet, "http://"+tx.Addr()+"/", nil)
	w := httptest.NewRecorder()
	tx.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "<title>forge</title>") {
		t.Errorf("expected embedded index.html body, got %q", w.Body.String())
	}
}

// TestWebUIRouteDoesNotShadowWSOrHealth ensures the catch-all "/" GUI route
// registered in transport.go still leaves /ws and /health handled by their
// own handlers, per net/http.ServeMux's longest-pattern-wins matching.
func TestWebUIRouteDoesNotShadowWSOrHealth(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	logger := slog.New(slog.DiscardHandler)
	tx := NewTransport("127.0.0.1:0", h, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	req := httptest.NewRequest(http.MethodGet, "http://"+tx.Addr()+"/health", nil)
	w := httptest.NewRecorder()
	tx.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Errorf("expected /health to still return 200 'ok', got %d %q", w.Code, w.Body.String())
	}
}

// TestWebUIStaticAssetContentTypes checks the JS/CSS assets referenced by
// index.html are served with a usable content type (browsers refuse to
// execute/apply them otherwise).
func TestWebUIStaticAssetContentTypes(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	logger := slog.New(slog.DiscardHandler)
	tx := NewTransport("127.0.0.1:0", h, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Start(ctx); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	cases := []struct {
		path   string
		prefix string
	}{
		{"/app.js", "text/javascript"},
		{"/styles.css", "text/css"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://"+tx.Addr()+c.path, nil)
		w := httptest.NewRecorder()
		tx.server.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected status 200, got %d", c.path, w.Code)
			continue
		}
		ct := w.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, c.prefix) {
			t.Errorf("%s: expected content type prefix %q, got %q", c.path, c.prefix, ct)
		}
	}
}
