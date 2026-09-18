package llm

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

// RetryableError wraps a provider-call failure that's worth retrying
// against a DIFFERENT model/provider — a rate limit, a transient upstream
// outage (502/503/504), or a network-level timeout/connection failure.
// Errors NOT wrapped this way (bad request, unauthorized, model not found,
// forbidden) are permanent: the same broken request would fail identically
// against any other model, so a fallback chain must not burn an attempt on
// them — see IsRetryable, and the fallback-chain caller in registry.go.
type RetryableError struct {
	// StatusCode is the HTTP status that triggered this (0 for a
	// network-level failure with no HTTP response at all, e.g. a dial
	// timeout or connection refused).
	StatusCode int
	// RetryAfter is the provider's own requested cooldown (from a
	// Retry-After header), or 0 when the provider didn't send one — callers
	// fall back to their own default cooldown in that case.
	RetryAfter time.Duration
	Err        error
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// IsRetryable reports whether err (possibly wrapped) represents a transient
// failure worth retrying against a different model.
func IsRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}

// RetryAfterOf returns the RetryableError's RetryAfter duration when err
// wraps one, or 0 otherwise (0 also just means "provider didn't say" —
// callers apply their own default cooldown either way).
func RetryAfterOf(err error) time.Duration {
	var re *RetryableError
	if errors.As(err, &re) {
		return re.RetryAfter
	}
	return 0
}

// parseRetryAfter reads a Retry-After response header (RFC 9110 §10.2.3):
// either an integer number of seconds, or an HTTP-date. Returns 0 (meaning
// "not specified") when the header is absent or malformed — never negative.
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
