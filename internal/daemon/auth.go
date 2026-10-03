package daemon

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
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

// HashToken returns SHA-256(token) as lowercase hex — the LEGACY stored
// form. Still accepted by verifyToken so configs written before
// HashPassword existed keep working, but `forge daemon set-password` now
// writes HashPassword's salted, slow hash instead: an unsalted SHA-256 of a
// human-chosen password is cheap to brute-force offline if the config
// file leaks.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// pbkdf2Prefix tags HashPassword's output format:
// pbkdf2-sha256$<iterations>$<salt b64>$<key b64>.
const pbkdf2Prefix = "pbkdf2-sha256$"

// pbkdf2Iterations follows OWASP's 2023+ guidance for PBKDF2-HMAC-SHA256.
// A verification costs a few hundred ms of CPU — paid on GUI login and on
// the FIRST Bearer request carrying a given token per daemon process (see
// Transport.verify's cache), never per message.
const pbkdf2Iterations = 600_000

// HashPassword returns a salted PBKDF2-HMAC-SHA256 hash of token in the
// self-describing pbkdf2-sha256$iter$salt$key form verifyToken accepts
// (stdlib crypto/pbkdf2: no new dependency, unlike argon2id).
func HashPassword(token string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, token, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return pbkdf2Prefix + strconv.Itoa(pbkdf2Iterations) + "$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key), nil
}

// GenerateToken returns a random 256-bit token (base64url, no padding) for
// `forge daemon set-password --generate`: stronger than any password a
// human would type, and meant to be pasted, not remembered.
func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// verifyToken reports whether token matches want, which is either a
// HashPassword string or a legacy HashToken hex digest. Comparisons are
// constant-time so response timing can't leak how many bytes matched.
func verifyToken(token, want string) bool {
	if want == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(want, pbkdf2Prefix); ok {
		parts := strings.Split(rest, "$")
		if len(parts) != 3 {
			return false
		}
		iter, err := strconv.Atoi(parts[0])
		if err != nil || iter <= 0 {
			return false
		}
		b64 := base64.RawStdEncoding
		salt, err1 := b64.DecodeString(parts[1])
		wantKey, err2 := b64.DecodeString(parts[2])
		if err1 != nil || err2 != nil || len(wantKey) == 0 {
			return false
		}
		got, err := pbkdf2.Key(sha256.New, token, salt, iter, len(wantKey))
		if err != nil {
			return false
		}
		return subtle.ConstantTimeCompare(got, wantKey) == 1
	}
	got := HashToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// verify checks token against the configured hash, remembering the
// SHA-256 of the last token that verified so repeat Bearer requests (every
// CLI command opens a new connection) skip the slow PBKDF2 path. The cache
// holds a digest, never the token, and is only ever populated by a
// successful full verification against the current hash.
func (t *Transport) verify(token string) bool {
	digest := sha256.Sum256([]byte(token))
	if cached, ok := t.verifiedDigest.Load().(*[32]byte); ok && cached != nil &&
		subtle.ConstantTimeCompare(cached[:], digest[:]) == 1 {
		return true
	}
	if !verifyToken(token, t.authTokenHash) {
		return false
	}
	t.verifiedDigest.Store(&digest)
	return true
}

// loginLimiter throttles failed POST /auth/login attempts per client IP so
// the GUI login can't be used to brute-force the password online.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	loginMaxFailures = 5
	loginWindow      = time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string][]time.Time)}
}

// recent returns ip's failures still inside the window, pruning old ones.
func (l *loginLimiter) recent(ip string, now time.Time) []time.Time {
	kept := l.failures[ip][:0]
	for _, ts := range l.failures[ip] {
		if now.Sub(ts) < loginWindow {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = kept
	return kept
}

func (l *loginLimiter) blocked(ip string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip, time.Now())) >= loginMaxFailures
}

func (l *loginLimiter) fail(ip string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.failures[ip] = append(l.recent(ip, now), now)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok && t.verify(tok) {
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
	ip := clientIP(r)
	if t.logins.blocked(ip) {
		http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !t.verify(body.Token) {
		t.logins.fail(ip)
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
