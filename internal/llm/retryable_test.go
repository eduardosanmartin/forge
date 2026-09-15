package llm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestParseRetryAfter_Seconds(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "30")
	if got := parseRetryAfter(h); got != 30*time.Second {
		t.Errorf("parseRetryAfter = %v, want 30s", got)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(45 * time.Second).UTC()
	h := http.Header{}
	h.Set("Retry-After", future.Format(http.TimeFormat))
	got := parseRetryAfter(h)
	// Allow a couple seconds of slack for formatting/rounding to whole seconds.
	if got < 40*time.Second || got > 46*time.Second {
		t.Errorf("parseRetryAfter(HTTP-date ~45s out) = %v, want ~45s", got)
	}
}

func TestParseRetryAfter_Missing(t *testing.T) {
	if got := parseRetryAfter(http.Header{}); got != 0 {
		t.Errorf("parseRetryAfter(no header) = %v, want 0", got)
	}
}

func TestParseRetryAfter_Malformed(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "not-a-number-or-date")
	if got := parseRetryAfter(h); got != 0 {
		t.Errorf("parseRetryAfter(malformed) = %v, want 0", got)
	}
}

func TestParseRetryAfter_NegativeSecondsClampedToZero(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "-5")
	if got := parseRetryAfter(h); got != 0 {
		t.Errorf("parseRetryAfter(-5) = %v, want 0 (never negative)", got)
	}
}

func TestIsRetryable(t *testing.T) {
	wrapped := &RetryableError{StatusCode: 429, Err: errors.New("rate limited")}
	if !IsRetryable(wrapped) {
		t.Error("IsRetryable(*RetryableError) = false, want true")
	}
	// Also true through an additional wrap layer (errors.As unwraps).
	doubleWrapped := fmt.Errorf("chat failed: %w", wrapped)
	if !IsRetryable(doubleWrapped) {
		t.Error("IsRetryable(wrapped further) = false, want true")
	}
	if IsRetryable(errors.New("plain error")) {
		t.Error("IsRetryable(plain error) = true, want false")
	}
	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) = true, want false")
	}
}

func TestRetryAfterOf(t *testing.T) {
	wrapped := &RetryableError{StatusCode: 429, RetryAfter: 12 * time.Second, Err: errors.New("rate limited")}
	if got := RetryAfterOf(wrapped); got != 12*time.Second {
		t.Errorf("RetryAfterOf = %v, want 12s", got)
	}
	if got := RetryAfterOf(errors.New("plain")); got != 0 {
		t.Errorf("RetryAfterOf(non-retryable) = %v, want 0", got)
	}
}

// --- per-provider HTTP status -> retryable classification ---
// Table-driven across all three provider kinds since the classification
// logic is intentionally identical (see each mapHTTPError's doc comment).

func TestMapHTTPError_Classification(t *testing.T) {
	cases := []struct {
		status        int
		wantRetryable bool
	}{
		{http.StatusTooManyRequests, true},      // 429
		{http.StatusBadGateway, true},           // 502
		{http.StatusServiceUnavailable, true},   // 503
		{http.StatusGatewayTimeout, true},       // 504
		{http.StatusBadRequest, false},          // 400
		{http.StatusUnauthorized, false},        // 401
		{http.StatusForbidden, false},           // 403
		{http.StatusNotFound, false},            // 404
		{http.StatusInternalServerError, false}, // 500 — deliberately NOT retryable (see mapHTTPError doc)
	}

	oai := &OpenAICompatibleProvider{}
	anthropic := &AnthropicProvider{}
	gemini := &GeminiProvider{}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("openai_compatible/%d", tc.status), func(t *testing.T) {
			err := oai.mapHTTPError(tc.status, []byte("body"), http.Header{})
			if got := IsRetryable(err); got != tc.wantRetryable {
				t.Errorf("IsRetryable(status %d) = %v, want %v (err: %v)", tc.status, got, tc.wantRetryable, err)
			}
		})
		t.Run(fmt.Sprintf("anthropic/%d", tc.status), func(t *testing.T) {
			err := anthropic.mapHTTPError(tc.status, []byte("body"), http.Header{})
			if got := IsRetryable(err); got != tc.wantRetryable {
				t.Errorf("IsRetryable(status %d) = %v, want %v (err: %v)", tc.status, got, tc.wantRetryable, err)
			}
		})
		t.Run(fmt.Sprintf("gemini/%d", tc.status), func(t *testing.T) {
			err := gemini.mapHTTPError(tc.status, []byte("body"), http.Header{})
			if got := IsRetryable(err); got != tc.wantRetryable {
				t.Errorf("IsRetryable(status %d) = %v, want %v (err: %v)", tc.status, got, tc.wantRetryable, err)
			}
		})
	}
}

func TestMapHTTPError_429CarriesRetryAfter(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "17")

	oai := &OpenAICompatibleProvider{}
	err := oai.mapHTTPError(http.StatusTooManyRequests, []byte("slow down"), h)
	if !IsRetryable(err) {
		t.Fatal("expected a retryable error")
	}
	if got := RetryAfterOf(err); got != 17*time.Second {
		t.Errorf("RetryAfterOf = %v, want 17s", got)
	}
}

func TestMapHTTPError_ErrorMessageUnchangedByWrapping(t *testing.T) {
	// The wrap must not alter what operators see in logs — same message
	// text as before RetryableError existed.
	oai := &OpenAICompatibleProvider{}
	err := oai.mapHTTPError(http.StatusTooManyRequests, []byte(`{"error":"slow down"}`), http.Header{})
	want := `rate limited (429): {"error":"slow down"}`
	if err.Error() != want {
		t.Errorf("error message = %q, want %q", err.Error(), want)
	}
}

func TestMapError_TimeoutIsRetryable(t *testing.T) {
	oai := &OpenAICompatibleProvider{}

	// A real *url.Error (what http.Client.Do actually returns on failure)
	// wrapping a net.Error whose Timeout() is true — exactly what a request
	// timeout looks like in practice.
	urlErr := &url.Error{Op: "Get", URL: "http://example.com", Err: fakeTimeoutErr{}}
	if err := oai.mapError(urlErr); !IsRetryable(err) {
		t.Errorf("mapError(timeout) should be retryable, got: %v", err)
	}
}

func TestMapError_ConnectionRefusedIsRetryable(t *testing.T) {
	oai := &OpenAICompatibleProvider{}

	// A *url.Error wrapping a non-timeout failure (e.g. connection refused)
	// is still retryable — a different model/provider isn't affected by
	// this host being unreachable.
	urlErr := &url.Error{Op: "Get", URL: "http://example.com", Err: errors.New("connection refused")}
	if err := oai.mapError(urlErr); !IsRetryable(err) {
		t.Errorf("mapError(connection refused) should be retryable, got: %v", err)
	}
}

func TestMapError_OtherFailuresAreNotRetryable(t *testing.T) {
	oai := &OpenAICompatibleProvider{}

	// Not a *url.Error and not context.DeadlineExceeded — mapError's
	// fallback branch, deliberately NOT wrapped as retryable.
	if err := oai.mapError(errors.New("marshal failed")); IsRetryable(err) {
		t.Errorf("mapError(generic error) should NOT be retryable, got: %v", err)
	}
}

// fakeTimeoutErr is a minimal net.Error stand-in whose Timeout() is true,
// for constructing a realistic *url.Error without a live network call.
type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }
