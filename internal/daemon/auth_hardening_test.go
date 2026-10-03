package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHashPasswordRoundTripAndSalted(t *testing.T) {
	a, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := HashPassword("s3cret")
	if !strings.HasPrefix(a, pbkdf2Prefix) {
		t.Fatalf("hash %q lacks the %q prefix", a, pbkdf2Prefix)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical: salt is not random")
	}
	if !verifyToken("s3cret", a) || !verifyToken("s3cret", b) {
		t.Fatal("correct password did not verify")
	}
	if verifyToken("wrong", a) {
		t.Fatal("wrong password verified")
	}
	if strings.Contains(a, "s3cret") {
		t.Fatal("hash leaks the raw password")
	}
}

func TestVerifyTokenAcceptsLegacySHA256(t *testing.T) {
	if !verifyToken("s3cret", HashToken("s3cret")) {
		t.Fatal("legacy SHA-256 hash no longer verifies: existing configs would lock users out")
	}
}

func TestVerifyTokenRejectsMalformedPBKDF2(t *testing.T) {
	for _, bad := range []string{pbkdf2Prefix, pbkdf2Prefix + "x$y$z", pbkdf2Prefix + "0$AAAA$AAAA", pbkdf2Prefix + "10$!!$AAAA"} {
		if verifyToken("anything", bad) {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestGenerateTokenIsRandomAndLong(t *testing.T) {
	a, _ := GenerateToken()
	b, _ := GenerateToken()
	if a == b || len(a) < 40 {
		t.Fatalf("generated tokens %q / %q are not unique 256-bit values", a, b)
	}
}

func TestTransportVerifyCachesOnlySuccessfulVerification(t *testing.T) {
	hash, _ := HashPassword("s3cret")
	tr := &Transport{}
	tr.SetAuth(hash)
	if tr.verify("wrong") {
		t.Fatal("wrong token verified")
	}
	if tr.verifiedDigest.Load() != nil {
		t.Fatal("a failed verification populated the cache")
	}
	if !tr.verify("s3cret") || !tr.verify("s3cret") {
		t.Fatal("correct token did not verify (first or cached path)")
	}
	if tr.verify("wrong") {
		t.Fatal("cache let a different token through")
	}
}

func TestLoginLimiterBlocksAfterRepeatedFailures(t *testing.T) {
	tr := newTestTransportForAuth()
	tr.SetAuth(HashToken("s3cret"))
	tr.logins = newLoginLimiter()
	login := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.7:5555"
		rec := httptest.NewRecorder()
		tr.handleAuthLogin(rec, req)
		return rec.Code
	}
	for i := 0; i < loginMaxFailures; i++ {
		if code := login(`{"token":"wrong"}`); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code %d, want 401", i+1, code)
		}
	}
	if code := login(`{"token":"s3cret"}`); code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures even the right password got %d, want 429", loginMaxFailures, code)
	}
}

// C6 regression: DNS rebinding — a page on evil.example resolving to
// 127.0.0.1 sends Host: evil.example; the loopback daemon must refuse it.
func TestLoopbackHostGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	guard := loopbackHostGuard(ok)
	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:8765", http.StatusOK},
		{"localhost:8765", http.StatusOK},
		{"LOCALHOST", http.StatusOK},
		{"[::1]:8765", http.StatusOK},
		{"evil.example:8765", http.StatusForbidden},
		{"evil.example", http.StatusForbidden},
		{"192.168.1.10:8765", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Host %q: code %d, want %d", tc.host, rec.Code, tc.want)
		}
	}
}
