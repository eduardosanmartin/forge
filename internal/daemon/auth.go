package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sessionCookieName is the cookie the GUI login flow sets after a
// successful POST /auth/login (RF-7.4). The CLI never uses cookies — it
// authenticates every request with an Authorization: Bearer <token> header
// instead, since it has the raw token directly (FORGE_DAEMON_TOKEN) rather
// than a browser session.
const sessionCookieName = "forge_session"

// sessionTTL bounds how long a GUI login session stays valid without
// reauthenticating. Sessions are checked lazily (on each request) rather
// than swept by a background goroutine — cheap enough at expected scale
// (a handful of interactive GUI users, not a public multi-tenant service).
const sessionTTL = 24 * time.Hour

// HashToken returns SHA-256(token) as lowercase hex, the form persisted by
// `forge daemon set-password` and compared against on every request. The
// raw token itself is never written to disk.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// verifyToken reports whether token hashes to want, using a constant-time
// comparison so response timing can't leak how many hash bytes matched.
func verifyToken(token, want string) bool {
	if want == "" {
		return false
	}
	got := HashToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// sessionStore tracks GUI login sessions issued by POST /auth/login. It is
// intentionally in-memory only: a daemon restart invalidates every session,
// which is fine — the GUI just shows the login form again.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // session id -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

// create mints a new random session id valid for sessionTTL.
func (s *sessionStore) create() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return id, nil
}

// valid reports whether id is a live, unexpired session, opportunistically
// evicting it if it just expired.
func (s *sessionStore) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	return true
}

func (s *sessionStore) revoke(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// authRequired reports whether the transport has a token configured at
// all. When it doesn't, every auth check is a no-op — this is what keeps
// the default loopback daemon behaving exactly as it did before RF-7.4.
func (t *Transport) authRequired() bool {
	return t.authTokenHash != ""
}

// authenticated reports whether r carries valid credentials: either a
// Bearer token matching the configured hash (the CLI's path) or a live
// session cookie from a prior /auth/login (the GUI's path).
func (t *Transport) authenticated(r *http.Request) bool {
	if !t.authRequired() {
		return true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok && verifyToken(tok, t.authTokenHash) {
			return true
		}
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && t.sessions.valid(c.Value) {
		return true
	}
	return false
}

// requireAuth wraps next so it 401s whenever authRequired() is true and the
// request carries neither a valid Bearer token nor a valid session cookie.
// A disabled/unconfigured token (the default) makes this a pure pass-through.
func (t *Transport) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !t.authenticated(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="forge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleAuthStatus lets a client (chiefly the GUI, before it has any
// credentials at all) discover whether it needs to authenticate. It is
// intentionally unauthenticated itself — knowing "yes/no" leaks nothing.
// Explicitly uncacheable: a browser that cached a stale "required":true from
// before an operator cleared the token would otherwise keep showing the
// login form long after the daemon stopped requiring one.
func (t *Transport) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"required": t.authRequired()})
}

// handleAuthLogin verifies a POSTed {"token": "..."} body against the
// configured hash and, on success, issues a session cookie (RF-7.4's
// "password de UI como mínimo viable" for the GUI, which cannot attach a
// Bearer header to its own WebSocket handshake).
func (t *Transport) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !t.authRequired() {
		http.Error(w, "auth is not configured on this daemon", http.StatusBadRequest)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !verifyToken(body.Token, t.authTokenHash) {
		// Deliberately generic: do not distinguish "wrong password" from any
		// other failure mode in the response.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := t.sessions.create()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAuthLogout revokes the caller's session cookie, if any.
func (t *Transport) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		t.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
	})
	w.WriteHeader(http.StatusOK)
}
