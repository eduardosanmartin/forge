// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// EmergencyState tracks global and per-session halt state.
type EmergencyState struct {
	mu          sync.RWMutex
	globalHalt  atomic.Bool
	sessionHalt map[string]*SessionHaltInfo
	logger      *slog.Logger
}

// SessionHaltInfo holds halt state for a single session.
type SessionHaltInfo struct {
	Halted  bool
	Reason  string             // "user" | "emergency" | "budget"
	TurnCtx context.CancelFunc // cancels in-flight turn
}

// NewEmergencyState creates a new emergency state tracker.
func NewEmergencyState(logger *slog.Logger) *EmergencyState {
	return &EmergencyState{
		sessionHalt: make(map[string]*SessionHaltInfo),
		logger:      logger,
	}
}

// IsGlobalHalted returns true if the global emergency halt is active.
func (e *EmergencyState) IsGlobalHalted() bool {
	return e.globalHalt.Load()
}

// HaltAll sets the global halt flag and cancels all in-flight turns.
func (e *EmergencyState) HaltAll(reason string) {
	e.globalHalt.Store(true)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, info := range e.sessionHalt {
		if info.TurnCtx != nil {
			info.TurnCtx()
		}
	}
	if e.logger != nil {
		e.logger.Warn("emergency halt all sessions", "reason", reason)
	}
}

// ResumeAll clears the global halt flag.
func (e *EmergencyState) ResumeAll() {
	e.globalHalt.Store(false)
	if e.logger != nil {
		e.logger.Info("emergency resume all sessions")
	}
}

// IsSessionHalted returns true if the session is halted (global or per-session).
func (e *EmergencyState) IsSessionHalted(sessionID string) bool {
	if e.globalHalt.Load() {
		return true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	info, ok := e.sessionHalt[sessionID]
	return ok && info.Halted
}

// HaltSession marks a session as halted and cancels its in-flight turn.
func (e *EmergencyState) HaltSession(sessionID, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	info, ok := e.sessionHalt[sessionID]
	if !ok {
		info = &SessionHaltInfo{}
		e.sessionHalt[sessionID] = info
	}
	info.Halted = true
	info.Reason = reason
	if info.TurnCtx != nil {
		info.TurnCtx()
	}
	if e.logger != nil {
		e.logger.Warn("session halted", "session_id", sessionID, "reason", reason)
	}
}

// ResumeSession clears the halt flag for a session.
func (e *EmergencyState) ResumeSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if info, ok := e.sessionHalt[sessionID]; ok {
		info.Halted = false
		info.Reason = ""
		info.TurnCtx = nil
	}
	if e.logger != nil {
		e.logger.Info("session resumed", "session_id", sessionID)
	}
}

// SetTurnContext stores the cancel function for an in-flight turn.
func (e *EmergencyState) SetTurnContext(sessionID string, cancel context.CancelFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	info, ok := e.sessionHalt[sessionID]
	if !ok {
		info = &SessionHaltInfo{}
		e.sessionHalt[sessionID] = info
	}
	info.TurnCtx = cancel
}

// ClearTurnContext removes the cancel function after turn completes.
func (e *EmergencyState) ClearTurnContext(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if info, ok := e.sessionHalt[sessionID]; ok {
		info.TurnCtx = nil
	}
}

// GetHaltReason returns the halt reason for a session.
func (e *EmergencyState) GetHaltReason(sessionID string) string {
	if e.globalHalt.Load() {
		return "emergency"
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if info, ok := e.sessionHalt[sessionID]; ok && info.Halted {
		return info.Reason
	}
	return ""
}

// GetAllHaltedSessions returns all session IDs that are currently halted.
func (e *EmergencyState) GetAllHaltedSessions() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var halted []string
	for id, info := range e.sessionHalt {
		if info.Halted {
			halted = append(halted, id)
		}
	}
	return halted
}
