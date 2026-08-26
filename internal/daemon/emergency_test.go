// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestEmergencyStateGlobalHalt(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	// Initially not halted
	if e.IsGlobalHalted() {
		t.Error("expected not halted initially")
	}

	// Halt all
	e.HaltAll("emergency")
	if !e.IsGlobalHalted() {
		t.Error("expected global halt to be active")
	}

	// Resume all
	e.ResumeAll()
	if e.IsGlobalHalted() {
		t.Error("expected global halt to be cleared")
	}
}

func TestEmergencyStateSessionHalt(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	sessionID := "session-123"

	// Initially not halted
	if e.IsSessionHalted(sessionID) {
		t.Error("expected session not halted initially")
	}

	// Halt session
	e.HaltSession(sessionID, "user")
	if !e.IsSessionHalted(sessionID) {
		t.Error("expected session to be halted")
	}
	if e.GetHaltReason(sessionID) != "user" {
		t.Errorf("expected reason 'user', got %q", e.GetHaltReason(sessionID))
	}

	// Resume session
	e.ResumeSession(sessionID)
	if e.IsSessionHalted(sessionID) {
		t.Error("expected session to be resumed")
	}
}

func TestEmergencyStateGlobalHaltAffectsSessions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	sessionID := "session-123"

	// Global halt should affect all sessions
	e.HaltAll("emergency")
	if !e.IsSessionHalted(sessionID) {
		t.Error("global halt should affect all sessions")
	}
	if e.GetHaltReason(sessionID) != "emergency" {
		t.Errorf("expected reason 'emergency', got %q", e.GetHaltReason(sessionID))
	}

	e.ResumeAll()
	if e.IsSessionHalted(sessionID) {
		t.Error("resume all should clear session halt")
	}
}

func TestEmergencyStateTurnContext(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	sessionID := "session-123"
	ctx, cancel := context.WithCancel(context.Background())

	e.SetTurnContext(sessionID, cancel)

	// Halt session should cancel the turn context
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()

	e.HaltSession(sessionID, "user")

	select {
	case <-done:
		// Expected - context was cancelled
	case <-time.After(100 * time.Millisecond):
		t.Error("expected turn context to be cancelled")
	}

	e.ClearTurnContext(sessionID)
}

func TestEmergencyStateGetAllHaltedSessions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	e.HaltSession("session-1", "user")
	e.HaltSession("session-2", "budget")
	e.HaltSession("session-3", "user")
	// session-4 not halted

	halted := e.GetAllHaltedSessions()
	if len(halted) != 3 {
		t.Errorf("expected 3 halted sessions, got %d: %v", len(halted), halted)
	}
}

func TestEmergencyStateMultipleHaltsSameSession(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	sessionID := "session-123"

	e.HaltSession(sessionID, "user")
	if !e.IsSessionHalted(sessionID) {
		t.Error("expected session halted")
	}

	// Halt again with different reason
	e.HaltSession(sessionID, "budget")
	if !e.IsSessionHalted(sessionID) {
		t.Error("expected session still halted")
	}
	if e.GetHaltReason(sessionID) != "budget" {
		t.Errorf("expected reason 'budget', got %q", e.GetHaltReason(sessionID))
	}
}

func TestEmergencyStateConcurrentAccess(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	e := NewEmergencyState(logger)

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(id int) {
			sessionID := "session-" + string(rune(id+'0'))
			e.HaltSession(sessionID, "user")
			e.ResumeSession(sessionID)
			e.HaltAll("test")
			e.ResumeAll()
			_ = e.IsSessionHalted(sessionID)
			_ = e.IsGlobalHalted()
			_ = e.GetHaltReason(sessionID)
			_ = e.GetAllHaltedSessions()
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}
