// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/routing"
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
	BranchSession(ctx context.Context, sourceID string, atSeq int, metadata map[string]any) (store.Session, error)
	MergeBranch(ctx context.Context, sourceID, targetID string) (store.Session, error)
	CompareSessions(ctx context.Context, aID, bID string) (*store.SessionCompare, error)
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
	store          StoreInterface
	llmReg         LLMRegistryInterface
	toolsReg       ToolsRegistryInterface
	emergency      *EmergencyState
	logger         *slog.Logger
	mu             sync.RWMutex
	sessions       map[string]*SessionState // active sessions with turn contexts
	agent          *agent.Agent
	v1Deps         agent.V1Deps
	cfg            *config.Config
	deltaPublisher func(sessionID string, notif *JSONRPCNotification)

	// RF-1.4 jobs queue: detached turns survive client disconnect and are
	// tracked as jobs (session+seq) with running/done/failed/canceled states.
	// In-memory for MVP; list/get/cancel reuses the detached ExecuteTurn context.
	jobsMu sync.RWMutex
	jobs   map[string]*Job
	jobSeq map[string]int // per-session monotonic counter for job IDs
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

// WithDeltaPublisher wires a broadcaster for live streaming deltas (WU3).
// The publisher is called for each text delta when llm.streaming is enabled.
// It is additive: nil disables the bridge with no side effects.
func WithDeltaPublisher(publisher func(sessionID string, notif *JSONRPCNotification)) SessionManagerOption {
	return func(m *SessionManager) {
		m.deltaPublisher = publisher
	}
}

// SetDeltaPublisher sets the delta broadcaster after construction (used by Daemon to wire Transport).
func (m *SessionManager) SetDeltaPublisher(publisher func(sessionID string, notif *JSONRPCNotification)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deltaPublisher = publisher
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
		jobs:      make(map[string]*Job),
		jobSeq:    make(map[string]int),
		cfg:       cfg,
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

	// RF-1.3: first-class spawn mechanism via tool (chosen over daemon RPC because
	// the agent loop already has tool-calling orchestration with perm fencing and
	// session-scoped context; a tool reuses that path with no new transport).
	mgr.wireSubagentTool()

	return mgr
}

// wireSubagentTool registers spawn_subagent when the concrete registry is available.
func (m *SessionManager) wireSubagentTool() {
	if m.agent == nil || m.toolsReg == nil {
		return
	}
	reg, ok := m.toolsReg.(*tools.Registry)
	if !ok {
		return
	}
	if _, exists := reg.Get("spawn_subagent"); exists {
		return
	}
	tool := tools.NewSpawnSubagentTool()
	tool.SetSpawner(func(ctx context.Context, req tools.SpawnRequest) (tools.Result, error) {
		parentID := tools.SessionIDFromContext(ctx)
		if parentID == "" {
			return tools.Result{Content: "ERROR: missing parent session id"}, nil
		}
		spec := agent.ChildSpec{
			Task:          req.Task,
			MaxIterations: req.MaxIterations,
			TokenBudget:   req.TokenBudget,
			FileBudget:    req.FileBudget,
			Provider:      req.Provider,
			Model:         req.Model,
		}
		child, err := m.agent.SpawnChild(ctx, parentID, spec)
		if err != nil {
			return tools.Result{Content: fmt.Sprintf("ERROR: spawn_subagent: %v", err)}, nil
		}
		// Summarized result back into parent transcript (role=tool message fencing is added by registry).
		// Keep concise but actionable: child session id, success flag, summary, and metrics.
		status := "success"
		if !child.Success {
			status = "failed"
			if child.Error != "" {
				status += ": " + child.Error
			}
		}
		content := fmt.Sprintf("subagent %s [%s] task=%q\nSUMMARY:\n%s\nMETRICS: iterations=%d tokens=%d tool_calls=%d",
			child.ChildSessionID, status, child.Task, child.Summary, child.Metrics.IterationCount, child.Metrics.TotalTokens, child.Metrics.ToolCallCount)
		metadata := map[string]any{
			"subagent_session_id": child.ChildSessionID,
			"parent_session_id":   child.ParentSessionID,
			"success":             child.Success,
			"task":                child.Task,
		}
		// Safety note: child inherits parent's permission floor (same Engine/registry, equal or narrowed — never wider);
		// tool output is fenced/redacted like any other turn (registry guarantees).
		return tools.Result{Content: content, Metadata: metadata}, nil
	})
	reg.Register(tool)
}

// AnchorStore returns the anchor store wired via WithV1Deps (RF-3.4), or
// nil when memory dependencies were not provided at construction.
func (m *SessionManager) AnchorStore() *anchor.AnchorStoreSQL {
	return m.v1Deps.AnchorStore
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

// BranchSession creates a new session branched from sourceID at atSeq.
func (m *SessionManager) BranchSession(ctx context.Context, sourceID string, atSeq int, metadata map[string]any) (store.Session, error) {
	session, err := m.store.BranchSession(ctx, sourceID, atSeq, metadata)
	if err != nil {
		return store.Session{}, err
	}
	if m.logger != nil {
		m.logger.Info("session branched", "source_id", sourceID, "branch_id", session.ID, "at_seq", atSeq)
	}
	return session, nil
}

// MergeBranch appends the source branch tail onto target per store semantics.
func (m *SessionManager) MergeBranch(ctx context.Context, sourceID, targetID string) (store.Session, error) {
	session, err := m.store.MergeBranch(ctx, sourceID, targetID)
	if err != nil {
		return store.Session{}, err
	}
	if m.logger != nil {
		m.logger.Info("session merged", "source_id", sourceID, "target_id", targetID)
	}
	return session, nil
}

// CompareSessions returns a side-by-side comparison of two sessions.
func (m *SessionManager) CompareSessions(ctx context.Context, aID, bID string) (*store.SessionCompare, error) {
	return m.store.CompareSessions(ctx, aID, bID)
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
	return m.executeTurn(ctx, sessionID, userMessage, "", v1Flags...)
}

// ExecuteTurnWithModelHint is ExecuteTurn plus a per-call model override
// (RF-11 manifest execution, sugerenciasDeClaude.md §5.6): modelHint is a
// routing.ModelRole name ("cheap"/"generation"/"reasoning" — matches
// run.Task.ModelHint's vocabulary exactly). When non-empty and the registry
// exposes a ModelRouter, it's resolved to a concrete model name and pinned
// for this one turn only (agent.TurnOptions.OverrideModel) — the session's
// own default model and any other turn in it are unaffected. An empty hint,
// or a registry without router support, behaves exactly like ExecuteTurn.
func (m *SessionManager) ExecuteTurnWithModelHint(ctx context.Context, sessionID, userMessage, modelHint string, v1Flags ...bool) ([]store.Message, error) {
	return m.executeTurn(ctx, sessionID, userMessage, modelHint, v1Flags...)
}

func (m *SessionManager) executeTurn(ctx context.Context, sessionID, userMessage, modelHint string, v1Flags ...bool) ([]store.Message, error) {
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

	// no_tools: set once at session creation (see client.ManifestDecomposer)
	// for a session that must never call a tool, e.g. the RF-11 manifest
	// decomposition turn — read-only here, unlike the v1 flags above it's
	// never resolved from RPC params, only from how the session was created.
	noTools := false
	if session.Metadata != nil {
		if v, ok := session.Metadata["no_tools"].(bool); ok {
			noTools = v
		}
	}

	// modelHint (ExecuteTurnWithModelHint only) resolves through the
	// registry's ModelRouter into a concrete per-turn model override. An
	// unrecognized hint or a registry without router support silently
	// resolves to "" — the turn just runs with no override, exactly like a
	// plain ExecuteTurn call, rather than failing the turn over what is
	// fundamentally a sizing hint, not a hard requirement.
	overrideModel := ""
	var overrideProvider llm.Provider
	if modelHint != "" {
		role := routing.ModelRole(modelHint)
		if rp, ok := m.llmReg.(routerProvider); ok {
			if router := rp.GetRouter(); router != nil {
				overrideModel = router.ModelForRole(role)
			}
		}
		// The resolved model can belong to ANY configured provider (RF-2.4/
		// 2.5 cost-based routing lets each role point at a different one) —
		// pin the provider that declared it too, or the call would still go
		// out over whatever provider the turn defaults to.
		if overrideModel != "" {
			if pp, ok := m.llmReg.(roleProviderResolver); ok {
				overrideProvider = pp.ProviderForRole(role)
			}
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

	// RF-1.4 full: turns survive client disconnect. The turn context is
	// detached from the request context (which is tied to the WebSocket
	// connection lifetime) — it lives until the agent finishes, the
	// per-turn timeout fires, or an explicit halt cancels it. This lets a
	// reconnecting client reattach via GetMessagesSince polling.
	turnCtx, turnCancel := context.WithCancel(context.Background())

	// RF-1.4 jobs queue: register detached turn as a job (session+seq).
	// Capture startSeq before the turn so follow can poll since that point.
	startSeq := 0
	if msgs, gErr := m.store.GetMessagesSince(ctx, sessionID, 0); gErr == nil {
		startSeq = len(msgs)
	}
	job := m.registerJob(sessionID, startSeq, turnCancel)

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

	var turnErr error
	defer func() {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		m.emergency.ClearTurnContext(sessionID)
		// Finalize job status (running -> done/failed/canceled).
		halted := m.emergency.IsSessionHalted(sessionID)
		m.finishJob(job, turnErr, halted)
	}()

	// Delegate to agent with v1 flags and optional streaming delta bridge (WU3).
	// Streaming is controlled by config llm.streaming (default OFF). When enabled,
	// the manager publishes text deltas via MessageDelta notifications so TUI clients
	// receive live updates. Tool calls still execute as before; Chat remains canonical
	// when streaming is disabled or provider lacks support.
	// Tool-call events (RF: live progress) broadcast unconditionally —
	// unlike OnDelta above, NOT gated behind streamingEnabled, since
	// manifest-driven task turns (client.ManifestExecutor) never enable
	// text-delta streaming but still benefit from live tool-call ticks
	// (forge run subscribes to these while its blocking RPC call waits).
	onToolEvent := func(toolCallID, name, status, errMsg string) {
		m.mu.RLock()
		pub := m.deltaPublisher
		m.mu.RUnlock()
		if pub == nil {
			return
		}
		payload := ToolCallEventPayload{SessionID: sessionID, ToolCallID: toolCallID, Name: name, Status: status, Error: errMsg}
		if notif, nErr := NewNotification(MethodToolCallEvent, payload); nErr == nil {
			pub(sessionID, notif)
		}
	}

	var result agent.TurnResult
	streamingEnabled := m.cfg != nil && m.cfg.LLM.Streaming.IsEnabled()
	if streamingEnabled && m.deltaPublisher != nil {
		turnStart := time.Now()
		var firstSent bool
		opts := agent.TurnOptions{
			StreamingEnabled: true,
			NoTools:          noTools,
			OverrideModel:    overrideModel,
			OverrideProvider: overrideProvider,
			OnToolEvent:      onToolEvent,
			OnDelta: func(delta string) {
				// Publish per-delta notification (additive, best-effort, non-blocking).
				// TTFT is emitted on first delta for observability.
				payload := MessageDeltaPayload{SessionID: sessionID, Delta: delta}
				if !firstSent {
					ttft := time.Since(turnStart).Milliseconds()
					payload.TTFTMs = &ttft
					firstSent = true
				}
				if notif, nErr := NewNotification(MethodMessageDelta, payload); nErr == nil {
					// Capture publisher under lock snapshot to avoid race if SetDeltaPublisher races.
					m.mu.RLock()
					pub := m.deltaPublisher
					m.mu.RUnlock()
					if pub != nil {
						pub(sessionID, notif)
					}
				}
			},
		}
		result, turnErr = m.agent.ExecuteTurnWithOptions(turnCtx, sessionID, userMessage, opts)
		// Also log TTFT from agent metrics if available (more precise than delta bridge).
		if result.Metrics.TTFTMs > 0 && m.logger != nil {
			m.logger.Debug("ttft", "session_id", sessionID, "ttft_ms", result.Metrics.TTFTMs)
		}
	} else if streamingEnabled {
		result, turnErr = m.agent.ExecuteTurnWithOptions(turnCtx, sessionID, userMessage, agent.TurnOptions{StreamingEnabled: true, NoTools: noTools, OverrideModel: overrideModel, OverrideProvider: overrideProvider, OnToolEvent: onToolEvent})
	} else {
		result, turnErr = m.agent.ExecuteTurnWithOptions(turnCtx, sessionID, userMessage, agent.TurnOptions{NoTools: noTools, OverrideModel: overrideModel, OverrideProvider: overrideProvider, OnToolEvent: onToolEvent})
	}
	if turnErr != nil {
		return result.Messages, turnErr
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

// routerProvider is implemented by an LLM registry that exposes its
// role/step model router (*llm.Registry does). Type-asserted the same way
// modelSetter is, rather than widening LLMRegistryInterface, so a registry
// without router support degrades to "no override" instead of failing to
// satisfy the interface at all.
type routerProvider interface {
	GetRouter() *routing.ModelRouter
}

// roleProviderResolver is implemented by an LLM registry that can name which
// configured provider owns a role's resolved model (*llm.Registry does).
// Type-asserted like routerProvider: a registry without it just resolves no
// provider override, so a model_hint turn falls back to whatever provider it
// would have used anyway rather than failing.
type roleProviderResolver interface {
	ProviderForRole(role routing.ModelRole) llm.Provider
}

// providerSwitcher matches registries that support hot-swapping the default
// provider and model together (*llm.Registry does).
type providerSwitcher interface {
	SwitchProviderAndModel(provider, model string) error
}

// allModelsLister matches registries that can enumerate every model across
// every configured provider — used by SwitchModel's bare-name fallback to
// find which OTHER provider (if any) has a model not in the current one.
type allModelsLister interface {
	ListAll() []llm.ModelInfo
}

// providerLister matches registries that support discovering configured
// providers and, per provider, a live (freshly refreshed) model catalog.
type providerLister interface {
	ListProviders() []llm.ProviderInfo
	ListProviderModels(name string) ([]string, error)
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

// SwitchModel hot-swaps the daemon's default model and, when sessionID is
// non-empty, records the choice in that session's metadata under the
// "model" key (and "provider" when a provider switch happened too). An
// empty sessionID skips both the session-existence check and the metadata
// write — the registry-level switch (the part that actually matters: every
// future session picks up the new default) still happens, for callers with
// no session in play at all (forge daemon set-provider). A non-empty
// sessionID that doesn't exist still fails, exactly as before.
//
// modelSpec accepts two forms:
//   - "provider/model" (matching forge fanout --models' own syntax) switches
//     BOTH the default provider and model atomically, validated against that
//     provider's live catalog (llm.Registry.SwitchProviderAndModel) — this
//     works even for a model that isn't declared in providers.<name>.models,
//     since the live catalog comes from the provider's own /models endpoint.
//   - a bare "model" name first tries the current default provider
//     (unchanged pre-existing behavior). If not found there, every OTHER
//     configured provider's cached catalog (llm.Registry.ListAll) is
//     searched: exactly one match elsewhere switches provider+model
//     automatically; more than one is reported as an ambiguity error naming
//     every provider that has it (use "provider/model" to disambiguate);
//     zero matches returns the original not-found error unchanged.
func (m *SessionManager) SwitchModel(ctx context.Context, sessionID, modelSpec string) error {
	if sessionID != "" {
		if _, err := m.store.GetSession(ctx, sessionID); err != nil {
			return fmt.Errorf("switch model: %w", err)
		}
	}

	provider, model := "", modelSpec
	if p, mdl, ok := strings.Cut(modelSpec, "/"); ok && m.isKnownProvider(p) {
		// Only split on "/" when the prefix actually names a configured
		// provider — otherwise this is a bare model name that happens to
		// contain a slash (e.g. a HuggingFace-style "org/model" catalog
		// name like "opencode/muse-spark-1.3-contributor-free"), and
		// splitting it here misreads "opencode" as a provider name,
		// producing a "provider not found" error for a model that's
		// actually declared under some other real provider. Falls through
		// to the bare-model-name path below, which resolves it correctly
		// via SetDefault/resolveModelAcrossProviders.
		provider, model = p, mdl
	}

	if provider != "" {
		switcher, ok := m.llmReg.(providerSwitcher)
		if !ok {
			return &ModelUnavailableError{Model: modelSpec, Err: errors.New("llm registry does not support provider switching")}
		}
		if err := switcher.SwitchProviderAndModel(provider, model); err != nil {
			return &ModelUnavailableError{Model: modelSpec, Err: err}
		}
	} else {
		setter, ok := m.llmReg.(modelSetter)
		if !ok {
			return &ModelUnavailableError{Model: model, Err: errors.New("llm registry does not support model switching")}
		}
		if err := setter.SetDefault(model); err != nil {
			resolved, rerr := m.resolveModelAcrossProviders(model, err)
			if rerr != nil {
				return rerr
			}
			provider = resolved
		}
	}

	if sessionID == "" {
		return nil
	}
	meta := map[string]any{"model": model}
	if provider != "" {
		meta["provider"] = provider
	}
	if err := m.store.UpdateSessionMetadata(ctx, sessionID, meta); err != nil {
		return fmt.Errorf("persist model choice: %w", err)
	}
	return nil
}

// resolveModelAcrossProviders is SwitchModel's fallback when a bare model
// name isn't in the current default provider (setErr): search every other
// provider's cached catalog for it. Returns the single provider name that
// has it (already switched via SwitchProviderAndModel) on a unique match,
// or an error — the original setErr unchanged on zero matches, a named
// ambiguity error on more than one.
func (m *SessionManager) resolveModelAcrossProviders(model string, setErr error) (string, error) {
	lister, lok := m.llmReg.(allModelsLister)
	switcher, sok := m.llmReg.(providerSwitcher)
	if !lok || !sok {
		return "", &ModelUnavailableError{Model: model, Err: setErr}
	}
	matches := make(map[string]bool)
	for _, mi := range lister.ListAll() {
		if mi.Name == model {
			matches[mi.Provider] = true
		}
	}
	switch len(matches) {
	case 0:
		return "", &ModelUnavailableError{Model: model, Err: setErr}
	case 1:
		var providerName string
		for p := range matches {
			providerName = p
		}
		if err := switcher.SwitchProviderAndModel(providerName, model); err != nil {
			return "", &ModelUnavailableError{Model: model, Err: err}
		}
		return providerName, nil
	default:
		names := make([]string, 0, len(matches))
		for p := range matches {
			names = append(names, p)
		}
		sort.Strings(names)
		return "", &ModelUnavailableError{Model: model, Err: fmt.Errorf(
			"model %q exists in multiple providers (%s) — use %q to disambiguate",
			model, strings.Join(names, ", "), fmt.Sprintf("%s/%s", names[0], model))}
	}
}

// isKnownProvider reports whether name matches a configured provider —
// used by SwitchModel to decide whether a "/" in modelSpec is explicit
// provider/model syntax or just part of a bare model name (see its call
// site). A registry that doesn't implement providerLister can't answer, so
// this conservatively says no: modelSpec is then treated as a bare model
// name end to end, which is the same behavior every registry had before
// SwitchModel's "provider/model" syntax existed.
func (m *SessionManager) isKnownProvider(name string) bool {
	lister, ok := m.llmReg.(providerLister)
	if !ok {
		return false
	}
	for _, p := range lister.ListProviders() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// ListProviders returns every configured provider's name and kind.
func (m *SessionManager) ListProviders() ([]llm.ProviderInfo, error) {
	lister, ok := m.llmReg.(providerLister)
	if !ok {
		return nil, errors.New("llm registry does not support provider listing")
	}
	return lister.ListProviders(), nil
}

// ListProviderModels returns the live model catalog for one named provider —
// see llm.Registry.ListProviderModels: this forces a fresh fetch rather than
// serving whatever was cached at daemon startup, so it surfaces every model
// the provider actually has right now, declared in config or not.
func (m *SessionManager) ListProviderModels(name string) ([]string, error) {
	lister, ok := m.llmReg.(providerLister)
	if !ok {
		return nil, errors.New("llm registry does not support provider listing")
	}
	return lister.ListProviderModels(name)
}
