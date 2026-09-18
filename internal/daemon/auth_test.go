package daemon

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHashTokenDeterministicAndDistinct(t *testing.T) {
	a := HashToken("correct horse battery staple")
	b := HashToken("correct horse battery staple")
	c := HashToken("something else")
	if a != b {
		t.Fatalf("same input produced different hashes: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different inputs produced the same hash")
	}
	if len(a) != 64 { // sha256 hex = 32 bytes = 64 hex chars
		t.Fatalf("hash length = %d, want 64", len(a))
	}
}

func TestVerifyToken(t *testing.T) {
	hash := HashToken("s3cret")
	if !verifyToken("s3cret", hash) {
		t.Error("correct token should verify")
	}
	if verifyToken("wrong", hash) {
		t.Error("wrong token should not verify")
	}
	if verifyToken("s3cret", "") {
		t.Error("an empty configured hash should never verify (auth effectively disabled)")
	}
}

func TestSessionStoreCreateValidateRevoke(t *testing.T) {
	s := newSessionStore()
	id, err := s.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !s.valid(id) {
		t.Fatal("freshly created session should be valid")
	}
	if s.valid("not-a-real-id") {
		t.Fatal("unknown session id should not be valid")
	}
	if s.valid("") {
		t.Fatal("empty session id should not be valid")
	}

	s.revoke(id)
	if s.valid(id) {
		t.Fatal("revoked session should no longer be valid")
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	s := newSessionStore()
	id, err := s.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Force it into the past instead of waiting out the real TTL.
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	if s.valid(id) {
		t.Fatal("expired session should not be valid")
	}
	s.mu.Lock()
	_, stillPresent := s.sessions[id]
	s.mu.Unlock()
	if stillPresent {
		t.Fatal("valid() should evict an expired session, not just report it invalid")
	}
}

func newTestTransportForAuth() *Transport {
	return &Transport{logger: slog.New(slog.DiscardHandler)}
}

func TestTransportAuthDisabledByDefault(t *testing.T) {
	tr := newTestTransportForAuth()
	if tr.authRequired() {
		t.Fatal("authRequired should be false with no token configured")
	}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if !tr.authenticated(req) {
		t.Fatal("a request with no credentials should be treated as authenticated when auth is disabled")
	}
}

func TestTransportAuthenticatedBearerToken(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.SetAuth(HashToken("s3cret"))
	tr.sessions = newSessionStore()

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if tr.authenticated(req) {
		t.Fatal("a request with no credentials should not authenticate once a token is configured")
	}

	req.Header.Set("Authorization", "Bearer wrong")
	if tr.authenticated(req) {
		t.Fatal("wrong bearer token should not authenticate")
	}

	req.Header.Set("Authorization", "Bearer s3cret")
	if !tr.authenticated(req) {
		t.Fatal("correct bearer token should authenticate")
	}
}

func TestTransportAuthenticatedSessionCookie(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.SetAuth(HashToken("s3cret"))
	tr.sessions = newSessionStore()
	id, err := tr.sessions.create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	if !tr.authenticated(req) {
		t.Fatal("a valid session cookie should authenticate")
	}

	bad := httptest.NewRequest(http.MethodGet, "/ws", nil)
	bad.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "garbage"})
	if tr.authenticated(bad) {
		t.Fatal("an invalid session cookie should not authenticate")
	}
}

func TestRequireAuthMiddleware(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.SetAuth(HashToken("s3cret"))
	tr.sessions = newSessionStore()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := tr.requireAuth(inner)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status = %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 response should carry a WWW-Authenticate header")
	}

	w2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("valid bearer token: status = %d, want 200", w2.Code)
	}
}

func TestHandleAuthStatus(t *testing.T) {
	tr := newTestTransportForAuth()
	w := httptest.NewRecorder()
	tr.handleAuthStatus(w, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if !strings.Contains(w.Body.String(), `"required":false`) {
		t.Errorf("expected required:false with no token configured, got %s", w.Body.String())
	}

	tr.SetAuth(HashToken("s3cret"))
	w2 := httptest.NewRecorder()
	tr.handleAuthStatus(w2, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if !strings.Contains(w2.Body.String(), `"required":true`) {
		t.Errorf("expected required:true once a token is configured, got %s", w2.Body.String())
	}
}

func TestHandleAuthLogin(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.sessions = newSessionStore()

	// Auth not configured at all.
	w := httptest.NewRecorder()
	tr.handleAuthLogin(w, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"token":"x"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("login with auth unconfigured: status = %d, want 400", w.Code)
	}

	tr.SetAuth(HashToken("s3cret"))

	// Wrong method.
	wGet := httptest.NewRecorder()
	tr.handleAuthLogin(wGet, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if wGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /auth/login: status = %d, want 405", wGet.Code)
	}

	// Wrong token.
	wBad := httptest.NewRecorder()
	tr.handleAuthLogin(wBad, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"token":"wrong"}`)))
	if wBad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", wBad.Code)
	}
	if len(wBad.Result().Cookies()) != 0 {
		t.Error("a failed login must not set a session cookie")
	}

	// Correct token.
	wOK := httptest.NewRecorder()
	tr.handleAuthLogin(wOK, httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"token":"s3cret"}`)))
	if wOK.Code != http.StatusOK {
		t.Fatalf("correct token: status = %d, want 200", wOK.Code)
	}
	cookies := wOK.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("expected a %s cookie, got %+v", sessionCookieName, cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if cookies[0].Secure {
		t.Error("session cookie should not be Secure over a request with no TLS (r.TLS == nil in this test)")
	}
	if !tr.sessions.valid(cookies[0].Value) {
		t.Error("the cookie's session id should be valid in the session store")
	}
}

func TestHandleAuthLogout(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.SetAuth(HashToken("s3cret"))
	tr.sessions = newSessionStore()
	id, _ := tr.sessions.create()

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	w := httptest.NewRecorder()
	tr.handleAuthLogout(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", w.Code)
	}
	if tr.sessions.valid(id) {
		t.Error("logout should revoke the session")
	}
}
