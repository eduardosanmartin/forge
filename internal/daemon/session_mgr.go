// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// StoreInterface defines the store operations needed by SessionManager.
type StoreInterface interface {
	CreateSession(ctx context.Context, metadata map[string]any) (store.Session, error)
	GetSession(ctx context.Context, id string) (store.Session, error)
	UpdateSessionMetadata(ctx context.Context, id string, metadata map[string]any) error
	ListSessions(ctx context.Context, limit, offset int) ([]store.Session, error)
	DeleteSession(ctx context.Context, id string) error
	AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error)
	GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error)
	GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error)
	Close() error
}

// LLMRegistryInterface defines the LLM registry operations needed by SessionManager.
type LLMRegistryInterface interface {
	GetDefault() (llm.Provider, string)
	Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
	Close() error
}

// ToolsRegistryInterface defines the tools registry operations needed by SessionManager.
type ToolsRegistryInterface interface {
	List() []tools.Tool
	Execute(ctx context.Context, name string, args map[string]any) (tools.Result, error)
}

// SessionManager manages sessions and executes agent turns.
type SessionManager struct {
	store     StoreInterface
	llmReg    LLMRegistryInterface
	toolsReg  ToolsRegistryInterface
	emergency *EmergencyState
	logger    *slog.Logger
	mu        sync.RWMutex
	sessions  map[string]*SessionState // active sessions with turn contexts
	agent     *agent.Agent
	v1Deps    agent.V1Deps
}

// SessionState holds runtime state for an active session.
type SessionState struct {
	Session    store.Session
	TurnCtx    context.Context
	TurnCancel context.CancelFunc
	mu         sync.Mutex
}

// SessionManagerOption configures optional SessionManager dependencies at
// construction time.
type SessionManagerOption func(*SessionManager)

// WithV1Deps wires the v1 feature dependencies (retriever, compactor,
// anchor store) into the session manager and its agent. Nil fields disable
// the corresponding behavior; the retriever is also used by the session
// manager itself to re-index transcripts after each turn.
func WithV1Deps(deps agent.V1Deps) SessionManagerOption {
	return func(m *SessionManager) {
		m.v1Deps = deps
		if m.agent != nil {
			m.agent.SetV1Deps(deps)
		}
	}
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager(
	store StoreInterface,
	llmReg LLMRegistryInterface,
	toolsReg ToolsRegistryInterface,
	emergency *EmergencyState,
	logger *slog.Logger,
	cfg *config.Config,
	permsEngine agent.PermsEngineInterface,
	storeImpl agent.StoreInterface,
	opts ...SessionManagerOption,
) *SessionManager {
	mgr := &SessionManager{
		store:     store,
		llmReg:    llmReg,
		toolsReg:  toolsReg,
		emergency: emergency,
		logger:    logger,
		sessions:  make(map[string]*SessionState),
	}

	// Create the agent if all dependencies are available
	if cfg != nil && llmReg != nil && toolsReg != nil && permsEngine != nil && storeImpl != nil {
		// Type assert to agent interfaces for agent creation
		if llmRegConcrete, ok := llmReg.(agent.LLMRegistryInterface); ok {
			if toolsRegConcrete, ok := toolsReg.(agent.ToolsRegistryInterface); ok {
				mgr.agent = agent.NewAgent(cfg, storeImpl, llmRegConcrete, toolsRegConcrete, permsEngine, logger)
			}
		}
	}

	for _, opt := range opts {
		opt(mgr)
	}

	return mgr
}

// CreateSession creates a new session.
func (m *SessionManager) CreateSession(ctx context.Context, metadata map[string]any) (store.Session, error) {
	session, err := m.store.CreateSession(ctx, metadata)
	if err != nil {
		return store.Session{}, err
	}
	if m.logger != nil {
		m.logger.Info("session created", "session_id", session.ID)
	}
	return session, nil
}

// GetSession retrieves a session by ID.
func (m *SessionManager) GetSession(ctx context.Context, id string) (store.Session, bool) {
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return store.Session{}, false
	}
	return session, true
}

// ListSessions returns all sessions.
func (m *SessionManager) ListSessions(ctx context.Context, limit, offset int) ([]store.Session, error) {
	return m.store.ListSessions(ctx, limit, offset)
}

// DeleteSession deletes a session.
func (m *SessionManager) DeleteSession(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cancel any in-flight turn
	if state, ok := m.sessions[id]; ok {
		state.mu.Lock()
		if state.TurnCancel != nil {
			state.TurnCancel()
		}
		state.mu.Unlock()
		delete(m.sessions, id)
	}

	m.emergency.ClearTurnContext(id)
	return m.store.DeleteSession(ctx, id)
}

// ExecuteTurn executes a single agent turn for a session.
// v1Flags can include: enableRetrieval, enableCompaction, enableAnchoring, enableRouting, enableSkills
func (m *SessionManager) ExecuteTurn(ctx context.Context, sessionID, userMessage string, v1Flags ...bool) ([]store.Message, error) {
	// Check if session exists
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	// Check halt state
	if m.emergency.IsSessionHalted(sessionID) {
		return nil, fmt.Errorf("session halted: %s", m.emergency.GetHaltReason(sessionID))
	}

	// Check if agent is available
	if m.agent == nil {
		return nil, fmt.Errorf("agent not initialized")
	}

	// Read v1 flags from session metadata if not provided
	enableRetrieval := false
	enableCompaction := false
	enableAnchoring := false
	enableRouting := false
	enableSkills := false
	if len(v1Flags) >= 5 {
		enableRetrieval = v1Flags[0]
		enableCompaction = v1Flags[1]
		enableAnchoring = v1Flags[2]
		enableRouting = v1Flags[3]
		enableSkills = v1Flags[4]
	} else if len(v1Flags) >= 4 {
		enableRetrieval = v1Flags[0]
		enableCompaction = v1Flags[1]
		enableAnchoring = v1Flags[2]
		enableRouting = v1Flags[3]
		// enableSkills remains false for 4-flag callers (backward-compatible)
	} else if session.ID != "" && session.Metadata != nil {
		// Fallback to session metadata
		if v, ok := session.Metadata["v1_retrieval"].(bool); ok {
			enableRetrieval = v
		}
		if v, ok := session.Metadata["v1_compaction"].(bool); ok {
			enableCompaction = v
		}
		if v, ok := session.Metadata["v1_anchoring"].(bool); ok {
			enableAnchoring = v
		}
		if v, ok := session.Metadata["v1_routing"].(bool); ok {
			enableRouting = v
		}
		if v, ok := session.Metadata["v1_skills"].(bool); ok {
			enableSkills = v
		}
	}

	// Persist the resolved v1 flags in session metadata for the agent's
	// ContextAssembler (compaction/anchoring injections) and the agent
	// loop's model selection (routing). The flags usually come from this
	// same metadata, so compare the four values first: an unchanged turn
	// must not hit SQLite again.
	if session.Metadata == nil {
		session.Metadata = make(map[string]any)
	}
	metadataChanged := session.Metadata["v1_retrieval"] != enableRetrieval ||
		session.Metadata["v1_compaction"] != enableCompaction ||
		session.Metadata["v1_anchoring"] != enableAnchoring ||
		session.Metadata["v1_routing"] != enableRouting ||
		session.Metadata["v1_skills"] != enableSkills
	if metadataChanged {
		session.Metadata["v1_retrieval"] = enableRetrieval
		session.Metadata["v1_compaction"] = enableCompaction
		session.Metadata["v1_anchoring"] = enableAnchoring
		session.Metadata["v1_routing"] = enableRouting
		session.Metadata["v1_skills"] = enableSkills
		_ = m.store.UpdateSessionMetadata(ctx, sessionID, session.Metadata)
	}

	// Create turn context with cancellation
	turnCtx, turnCancel := context.WithCancel(ctx)

	// Register turn context for emergency cancellation
	m.emergency.SetTurnContext(sessionID, turnCancel)

	m.mu.Lock()
	state := &SessionState{
		Session:    session,
		TurnCtx:    turnCtx,
		TurnCancel: turnCancel,
	}
	m.sessions[sessionID] = state
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		m.emergency.ClearTurnContext(sessionID)
	}()

	// Delegate to agent with v1 flags
	// We need to pass v1 flags to the agent - for now we'll use the agent's ExecuteTurn
	// which will read flags from session metadata
	result, err := m.agent.ExecuteTurn(turnCtx, sessionID, userMessage)
	if err != nil {
		return result.Messages, err
	}

	// v1 retrieval indexing (best-effort): now that this turn's messages
	// are persisted, rebuild the retrieval index for the session. An index
	// failure is logged and never fails the completed turn.
	if enableRetrieval && m.v1Deps.Retriever != nil {
		m.indexSession(turnCtx, sessionID)
	}

	return result.Messages, nil
}

// indexSession rebuilds the retrieval index for a session from the full
// persisted transcript. Retriever.Index clears and rebuilds, so a full
// re-index keeps the operation stateless: the retrieval flag can be
// enabled at any point in a session and the next turn still sees the whole
// history. The index itself is in-memory per daemon process (v1). Best
// effort: failures are logged and swallowed.
func (m *SessionManager) indexSession(ctx context.Context, sessionID string) {
	msgs, err := m.store.GetMessagesSince(ctx, sessionID, 0)
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("v1 retrieval: fetch transcript for indexing failed",
				"session_id", sessionID, "error", err)
		}
		return
	}
	index := make([]retrieval.Message, 0, len(msgs))
	for _, msg := range msgs {
		index = append(index, retrieval.Message{
			ID:      msg.ID,
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	if err := m.v1Deps.Retriever.Index(index); err != nil {
		if m.logger != nil {
			m.logger.Warn("v1 retrieval: indexing failed",
				"session_id", sessionID, "error", err)
		}
	}
}

// GetMessages returns messages for a session.
func (m *SessionManager) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	return m.store.GetMessages(ctx, sessionID, limit, offset)
}

// GetMessagesSince returns messages for a session since a given sequence number.
func (m *SessionManager) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	return m.store.GetMessagesSince(ctx, sessionID, sinceSeq)
}

// HaltSession halts a session.
func (m *SessionManager) HaltSession(sessionID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.sessions[sessionID]; ok {
		state.mu.Lock()
		if state.TurnCancel != nil {
			state.TurnCancel()
		}
		state.mu.Unlock()
	}

	m.emergency.HaltSession(sessionID, reason)

	// Persist halt state in session metadata
	ctx := context.Background()
	return m.store.UpdateSessionMetadata(ctx, sessionID, map[string]any{
		"halted":      true,
		"halt_reason": reason,
	})
}

// ResumeSession resumes a halted session.
func (m *SessionManager) ResumeSession(sessionID string) error {
	m.emergency.ResumeSession(sessionID)

	// Clear halt state in session metadata
	ctx := context.Background()
	return m.store.UpdateSessionMetadata(ctx, sessionID, map[string]any{
		"halted":      false,
		"halt_reason": "",
	})
}

// GetActiveSessions returns the number of sessions with active turns.
func (m *SessionManager) GetActiveSessions() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// DefaultModel returns the name of the current default model, or "" when no
// registry is available.
func (m *SessionManager) DefaultModel() string {
	if m.llmReg == nil {
		return ""
	}
	_, model := m.llmReg.GetDefault()
	return model
}

// ModelUnavailableError reports that the requested model is not offered by
// the default provider. The handler maps it to a typed JSON-RPC error.
type ModelUnavailableError struct {
	Model string
	Err   error
}

func (e *ModelUnavailableError) Error() string {
	return fmt.Sprintf("model %q not available: %v", e.Model, e.Err)
}

func (e *ModelUnavailableError) Unwrap() error { return e.Err }

// modelSetter matches registries that support hot-swapping the default model.
type modelSetter interface {
	SetDefault(model string) error
}

// MarkSuccess marks a session as human-verified successful (RF-4.4 input gate).
// It sets session metadata "success"=true via the store's merge semantics.
func (m *SessionManager) MarkSuccess(ctx context.Context, sessionID string) error {
	if _, err := m.store.GetSession(ctx, sessionID); err != nil {
		return fmt.Errorf("mark success: %w", err)
	}
	if err := m.store.UpdateSessionMetadata(ctx, sessionID, map[string]any{"success": true}); err != nil {
		return fmt.Errorf("mark success: %w", err)
	}
	return nil
}

// SwitchModel hot-swaps the daemon's default model and records the choice in
// the session metadata under the "model" key. It fails if the session does
// not exist or the registry rejects the model.
func (m *SessionManager) SwitchModel(ctx context.Context, sessionID, model string) error {
	if _, err := m.store.GetSession(ctx, sessionID); err != nil {
		return fmt.Errorf("switch model: %w", err)
	}

	setter, ok := m.llmReg.(modelSetter)
	if !ok {
		return &ModelUnavailableError{Model: model, Err: errors.New("llm registry does not support model switching")}
	}
	if err := setter.SetDefault(model); err != nil {
		return &ModelUnavailableError{Model: model, Err: err}
	}

	if err := m.store.UpdateSessionMetadata(ctx, sessionID, map[string]any{"model": model}); err != nil {
		return fmt.Errorf("persist model choice: %w", err)
	}
	return nil
}
