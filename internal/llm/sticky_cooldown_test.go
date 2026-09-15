package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestResolveAttempts_SkipsCoolingEntry confirms a cooling entry is excluded
// while at least one candidate isn't cooling, and reappears once its
// cooldown expires.
func TestResolveAttempts_SkipsCoolingEntry(t *testing.T) {
	reg, _ := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})

	fakeNow := time.Now()
	reg.clock = func() time.Time { return fakeNow }
	reg.RecordFailure("backup", "b1", &RetryableError{Err: errors.New("boom")})

	attempts := reg.ResolveAttempts()
	if len(attempts) != 1 || attempts[0].ProviderName != "primary" {
		t.Fatalf("attempts = %+v, want just primary (backup cooling)", attempts)
	}

	fakeNow = fakeNow.Add(defaultCooldown + time.Second)
	attempts = reg.ResolveAttempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v, want both once backup's cooldown expires", attempts)
	}
}

// TestResolveAttempts_AllCoolingIgnoresCooldown confirms a wrong/over-long
// Retry-After can never wedge every candidate: with nothing fresh,
// ResolveAttempts falls back to trying everything anyway.
func TestResolveAttempts_AllCoolingIgnoresCooldown(t *testing.T) {
	reg, _ := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})

	fakeNow := time.Now()
	reg.clock = func() time.Time { return fakeNow }
	reg.RecordFailure("primary", "p1", &RetryableError{Err: errors.New("x")})
	reg.RecordFailure("backup", "b1", &RetryableError{Err: errors.New("y")})

	attempts := reg.ResolveAttempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v, want both (every candidate cooling -> ignore cooldowns)", attempts)
	}
}

// TestRecordFailure_UsesRetryAfterWhenPresent confirms a provider-supplied
// Retry-After overrides defaultCooldown.
func TestRecordFailure_UsesRetryAfterWhenPresent(t *testing.T) {
	reg, _ := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})

	fakeNow := time.Now()
	reg.clock = func() time.Time { return fakeNow }
	reg.RecordFailure("primary", "p1", &RetryableError{Err: errors.New("rl"), RetryAfter: 5 * time.Second})

	fakeNow = fakeNow.Add(3 * time.Second)
	if attempts := reg.ResolveAttempts(); len(attempts) != 1 {
		t.Fatalf("attempts at +3s = %+v, want just backup (still within the 5s Retry-After)", attempts)
	}

	fakeNow = fakeNow.Add(3 * time.Second) // total +6s, past the 5s Retry-After
	if attempts := reg.ResolveAttempts(); len(attempts) != 2 {
		t.Fatalf("attempts at +6s = %+v, want both (Retry-After expired)", attempts)
	}
}

// TestRecordSuccess_PromotesStickyDefault confirms a successful non-default
// attempt becomes the new default and sorts first in ResolveAttempts.
func TestRecordSuccess_PromotesStickyDefault(t *testing.T) {
	reg, _ := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})

	reg.RecordSuccess("backup", "b1")

	if _, model := reg.GetDefault(); model != "b1" {
		t.Fatalf("default model = %q, want b1", model)
	}
	attempts := reg.ResolveAttempts()
	if len(attempts) == 0 || attempts[0].ProviderName != "backup" {
		t.Fatalf("attempts[0] = %+v, want backup first (sticky)", attempts)
	}
}

// TestRecordSuccess_ClearsOwnCooldown confirms a success supersedes a stale
// cooldown on the same provider/model (e.g. it recovered right before this
// attempt was tried).
func TestRecordSuccess_ClearsOwnCooldown(t *testing.T) {
	reg, _ := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})

	fakeNow := time.Now()
	reg.clock = func() time.Time { return fakeNow }
	reg.RecordFailure("backup", "b1", &RetryableError{Err: errors.New("x")})
	reg.RecordSuccess("backup", "b1")

	for _, a := range reg.ResolveAttempts() {
		if a.ProviderName == "backup" && a.Model == "b1" {
			return
		}
	}
	t.Fatal("backup missing from attempts after RecordSuccess should have cleared its cooldown")
}

// TestChatWithFallback_StickyAvoidsRetryingBrokenDefaultOnNextCall is the
// end-to-end version of TestRecordSuccess_PromotesStickyDefault: after a
// real ChatWithFallback call falls over successfully, the NEXT call must go
// straight to the model that worked — never re-paying the failed default's
// round trip on every subsequent turn.
func TestChatWithFallback_StickyAvoidsRetryingBrokenDefaultOnNextCall(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup": {"b1"}},
		[]string{"backup/b1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))
	mocks["backup"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "b1", Content: "from backup", FinishReason: "stop"}).Build()))

	if _, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if chatRequestCount(mocks["primary"]) != 1 {
		t.Fatalf("primary got %d requests after first call, want 1", chatRequestCount(mocks["primary"]))
	}

	resp, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi again"}}})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if resp.Choices[0].Message.Content != "from backup" {
		t.Errorf("content = %q, want \"from backup\"", resp.Choices[0].Message.Content)
	}
	if chatRequestCount(mocks["primary"]) != 1 {
		t.Errorf("primary got %d requests after second call, want still 1 (sticky must skip it)", chatRequestCount(mocks["primary"]))
	}
	if chatRequestCount(mocks["backup"]) != 2 {
		t.Errorf("backup got %d requests, want 2 (one per call)", chatRequestCount(mocks["backup"]))
	}
}

// TestChatWithFallback_FailedAttemptsEnterCooldown combines sticky + cooldown
// in one realistic scenario: primary and backup1 both fail retryably,
// backup2 succeeds. After the call, primary and backup1 must be absent from
// ResolveAttempts (cooling) and backup2 must be the sole, sticky default.
func TestChatWithFallback_FailedAttemptsEnterCooldown(t *testing.T) {
	reg, mocks := chainTestSetup(t, "primary",
		map[string][]string{"primary": {"p1"}, "backup1": {"b1"}, "backup2": {"c1"}},
		[]string{"backup1/b1", "backup2/c1"})
	mocks["primary"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusTooManyRequests, Message: "rate limited"}).Build()))
	mocks["backup1"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ErrorResponseBuilder{StatusCode: http.StatusServiceUnavailable, Message: "down"}).Build()))
	mocks["backup2"].SetHandler("/v1/chat/completions", fixedHandler(
		(&ChatResponseBuilder{Model: "c1", Content: "from backup2", FinishReason: "stop"}).Build()))

	fakeNow := time.Now()
	reg.clock = func() time.Time { return fakeNow }

	if _, err := reg.ChatWithFallback(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("ChatWithFallback: %v", err)
	}

	attempts := reg.ResolveAttempts()
	for _, a := range attempts {
		if a.ProviderName == "primary" || a.ProviderName == "backup1" {
			t.Errorf("attempts still include cooling entry %s/%s", a.ProviderName, a.Model)
		}
	}
	if len(attempts) != 1 || attempts[0].ProviderName != "backup2" {
		t.Errorf("attempts = %+v, want just backup2 (sticky default, the other two cooling)", attempts)
	}
}
