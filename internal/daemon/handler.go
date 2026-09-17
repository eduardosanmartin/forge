// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/eduardosanmartin/forge/internal/cost"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
)

// Handler dispatches JSON-RPC requests to the session manager.
type Handler struct {
	mgr       *SessionManager
	logger    *slog.Logger
	pluginMgr *pluginwasm.Manager
	skillMgr  *skill.Manager
}

// NewHandler creates a new Handler.
func NewHandler(mgr *SessionManager, logger *slog.Logger, pluginMgr *pluginwasm.Manager, skillMgr *skill.Manager) *Handler {
	return &Handler{mgr: mgr, logger: logger, pluginMgr: pluginMgr, skillMgr: skillMgr}
}

// HandleRequest processes a JSON-RPC request and returns a response (or nil for notifications).
func (h *Handler) HandleRequest(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if req.JSONRPC != "2.0" {
		return NewErrorResponse(req.ID, ErrCodeInvalidRequest, "jsonrpc must be \"2.0\"", nil)
	}

	switch req.Method {
	case MethodCreateSession:
		return h.handleCreateSession(ctx, req)
	case MethodGetSession:
		return h.handleGetSession(ctx, req)
	case MethodListSessions:
		return h.handleListSessions(ctx, req)
	case MethodDeleteSession:
		return h.handleDeleteSession(ctx, req)
	case MethodBranchSession:
		return h.handleBranchSession(ctx, req)
	case MethodMergeSession:
		return h.handleMergeSession(ctx, req)
	case MethodCompareSessions:
		return h.handleCompareSessions(ctx, req)
	case MethodSessionCost:
		return h.handleSessionCost(ctx, req)
	case MethodCostSummary:
		return h.handleCostSummary(ctx, req)
	case MethodExecuteTurn:
		return h.handleExecuteTurn(ctx, req)
	case MethodGetMessages:
		return h.handleGetMessages(ctx, req)
	case MethodGetMessagesSince:
		return h.handleGetMessagesSince(ctx, req)
	case MethodHaltSession:
		return h.handleHaltSession(ctx, req)
	case MethodResumeSession:
		return h.handleResumeSession(ctx, req)
	case MethodHaltAll:
		return h.handleHaltAll(ctx, req)
	case MethodStatus:
		return h.handleStatus(ctx, req)
	case MethodSwitchModel:
		return h.handleSwitchModel(ctx, req)
	case MethodProviderList:
		return h.handleProviderList(ctx, req)
	case MethodProviderListModels:
		return h.handleProviderListModels(ctx, req)
	case MethodProviderSwitch:
		return h.handleProviderSwitch(ctx, req)
	case MethodSessionMarkSuccess:
		return h.handleMarkSuccess(ctx, req)
	case MethodPluginList:
		return h.handlePluginList(ctx, req)
	case MethodPluginEnable:
		return h.handlePluginEnable(ctx, req)
	case MethodPluginDisable:
		return h.handlePluginDisable(ctx, req)
	case MethodPluginReload:
		return h.handlePluginReload(ctx, req)
	case MethodSkillList:
		return h.handleSkillList(ctx, req)
	case MethodSkillEnable:
		return h.handleSkillEnable(ctx, req)
	case MethodSkillDisable:
		return h.handleSkillDisable(ctx, req)
	case MethodSkillReload:
		return h.handleSkillReload(ctx, req)
	case MethodJobList:
		return h.handleJobList(ctx, req)
	case MethodJobGet:
		return h.handleJobGet(ctx, req)
	case MethodJobCancel:
		return h.handleJobCancel(ctx, req)
	case MethodMemoryList:
		return h.handleMemoryList(ctx, req)
	case MethodMemoryGet:
		return h.handleMemoryGet(ctx, req)
	case MethodMemoryCreate:
		return h.handleMemoryCreate(ctx, req)
	case MethodMemoryUpdate:
		return h.handleMemoryUpdate(ctx, req)
	case MethodMemoryDelete:
		return h.handleMemoryDelete(ctx, req)
	case MethodFanout:
		return h.handleFanout(ctx, req)
	case MethodRunStart:
		return h.handleRunStart(ctx, req)
	case MethodRunStatus:
		return h.handleRunStatus(ctx, req)
	case MethodRunList:
		return h.handleRunList(ctx, req)
	case MethodRunResume:
		return h.handleRunResume(ctx, req)
	case MethodRunCancel:
		return h.handleRunCancel(ctx, req)
	case MethodRunApproveCheckpoint:
		return h.handleRunApproveCheckpoint(ctx, req)
	default:
		return NewErrorResponse(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method), nil)
	}
}

func (h *Handler) handleCreateSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params CreateSessionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		}
	}

	session, err := h.mgr.CreateSession(ctx, params.Metadata)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "create session failed", err.Error())
	}

	result := SessionResult{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Metadata:  session.Metadata,
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleGetSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params GetSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	session, ok := h.mgr.GetSession(ctx, params.SessionID)
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
	}

	result := SessionResult{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Metadata:  session.Metadata,
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleListSessions(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ListSessionsParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		}
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}

	sessions, err := h.mgr.ListSessions(ctx, params.Limit, params.Offset)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "list sessions failed", err.Error())
	}

	result := ListSessionsResult{Sessions: make([]SessionResult, len(sessions))}
	for i, s := range sessions {
		result.Sessions[i] = SessionResult{
			ID:        s.ID,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			Metadata:  s.Metadata,
		}
		// Message count enrichment loads each session's transcript. Acceptable
		// for v0 local scale (SQLite, capped page size); revisit with a
		// dedicated COUNT query if session histories grow large.
		if msgs, err := h.mgr.GetMessagesSince(ctx, s.ID, 0); err == nil {
			result.Sessions[i].MessageCount = len(msgs)
		}
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleDeleteSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params DeleteSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	if err := h.mgr.DeleteSession(ctx, params.SessionID); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "delete session failed", err.Error())
	}

	return h.resultResponse(req.ID, map[string]any{"deleted": true})
}

func (h *Handler) handleBranchSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params BranchSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SourceSessionID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "source_session_id is required", nil)
	}
	if params.AtSeq < 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "at_seq must be >= 0", nil)
	}
	session, err := h.mgr.BranchSession(ctx, params.SourceSessionID, params.AtSeq, params.Metadata)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "source session not found", nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, "branch session failed", err.Error())
	}
	result := SessionResult{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Metadata:  session.Metadata,
	}
	if msgs, err := h.mgr.GetMessagesSince(ctx, session.ID, 0); err == nil {
		result.MessageCount = len(msgs)
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleMergeSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params MergeSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SourceSessionID == "" || params.TargetSessionID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "source_session_id and target_session_id are required", nil)
	}
	session, err := h.mgr.MergeBranch(ctx, params.SourceSessionID, params.TargetSessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		}
		if err.Error() == "merge: source and target must differ" {
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, err.Error(), nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, "merge session failed", err.Error())
	}
	result := SessionResult{
		ID:        session.ID,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
		Metadata:  session.Metadata,
	}
	if msgs, err := h.mgr.GetMessagesSince(ctx, session.ID, 0); err == nil {
		result.MessageCount = len(msgs)
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleCompareSessions(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params CompareSessionsParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SessionA == "" || params.SessionB == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "session_a and session_b are required", nil)
	}
	cmp, err := h.mgr.CompareSessions(ctx, params.SessionA, params.SessionB)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, "compare sessions failed", err.Error())
	}
	result := CompareSessionsResult{
		SessionA: SessionResult{
			ID:           cmp.SessionA.ID,
			CreatedAt:    cmp.SessionA.CreatedAt,
			UpdatedAt:    cmp.SessionA.UpdatedAt,
			Metadata:     cmp.SessionA.Metadata,
			MessageCount: cmp.CountA,
		},
		SessionB: SessionResult{
			ID:           cmp.SessionB.ID,
			CreatedAt:    cmp.SessionB.CreatedAt,
			UpdatedAt:    cmp.SessionB.UpdatedAt,
			Metadata:     cmp.SessionB.Metadata,
			MessageCount: cmp.CountB,
		},
		BranchAtSeqA:    cmp.BranchAtSeqA,
		BranchAtSeqB:    cmp.BranchAtSeqB,
		BranchParentA:   cmp.BranchParentA,
		BranchParentB:   cmp.BranchParentB,
		BranchRootA:     cmp.BranchRootA,
		BranchRootB:     cmp.BranchRootB,
		CountA:          cmp.CountA,
		CountB:          cmp.CountB,
		DivergentCountA: cmp.DivergentCountA,
		DivergentCountB: cmp.DivergentCountB,
		SameSession:     cmp.SameSession,
	}
	for _, m := range cmp.DivergentA {
		result.DivergentA = append(result.DivergentA, messageToResult(m))
	}
	for _, m := range cmp.DivergentB {
		result.DivergentB = append(result.DivergentB, messageToResult(m))
	}
	if len(cmp.DivergentA) > 0 {
		last := messageToResult(cmp.DivergentA[len(cmp.DivergentA)-1])
		result.LastMessageA = &last
	} else if cmp.CountA > 0 {
		// fallback to last of full transcript when no divergent tail (same session)
		if msgs, err := h.mgr.GetMessagesSince(ctx, cmp.SessionA.ID, 0); err == nil && len(msgs) > 0 {
			last := messageToResult(msgs[len(msgs)-1])
			result.LastMessageA = &last
		}
	}
	if len(cmp.DivergentB) > 0 {
		last := messageToResult(cmp.DivergentB[len(cmp.DivergentB)-1])
		result.LastMessageB = &last
	} else if cmp.CountB > 0 {
		if msgs, err := h.mgr.GetMessagesSince(ctx, cmp.SessionB.ID, 0); err == nil && len(msgs) > 0 {
			last := messageToResult(msgs[len(msgs)-1])
			result.LastMessageB = &last
		}
	}
	return h.resultResponse(req.ID, result)
}

// handleSessionCost estimates one session's token cost (RNF-6.3). See
// internal/cost's package doc for the provider-attribution limitation this
// inherits: forge does not record which provider produced a message, so the
// estimate is attributed to the daemon's configured default provider.
func (h *Handler) handleSessionCost(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params SessionCostParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SessionID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "session_id is required", nil)
	}
	sess, ok := h.mgr.GetSession(ctx, params.SessionID)
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
	}
	messages, err := h.mgr.GetMessagesSince(ctx, params.SessionID, 0)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "get messages failed", err.Error())
	}
	result := cost.EstimateSessionCost(params.SessionID, sess.Metadata, messages, h.mgr.cfg.Providers, h.mgr.cfg.DefaultProvider)
	return h.resultResponse(req.ID, result)
}

// handleCostSummary aggregates estimated cost across sessions, grouped by
// (attributed) provider (RNF-6.3).
func (h *Handler) handleCostSummary(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params CostSummaryParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
		}
	}
	if params.Limit <= 0 {
		params.Limit = 200
	}
	sessions, err := h.mgr.ListSessions(ctx, params.Limit, params.Offset)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "list sessions failed", err.Error())
	}

	costs := make([]cost.SessionCost, 0, len(sessions))
	for _, sess := range sessions {
		messages, err := h.mgr.GetMessagesSince(ctx, sess.ID, 0)
		if err != nil {
			return NewErrorResponse(req.ID, ErrCodeInternalError, "get messages failed", err.Error())
		}
		costs = append(costs, cost.EstimateSessionCost(sess.ID, sess.Metadata, messages, h.mgr.cfg.Providers, h.mgr.cfg.DefaultProvider))
	}
	return h.resultResponse(req.ID, CostSummaryResult{Providers: cost.AggregateByProvider(costs)})
}

func (h *Handler) handleExecuteTurn(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ExecuteTurnParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	messages, err := h.mgr.ExecuteTurnWithModelHint(ctx, params.SessionID, params.UserMessage, params.ModelHint,
		params.EnableRetrieval, params.EnableCompaction, params.EnableAnchoring, params.EnableRouting, params.EnableSkills)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		case strings.HasPrefix(err.Error(), "session halted:"):
			return NewErrorResponse(req.ID, ErrCodeSessionHalted, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}

	result := ExecuteTurnResult{Messages: make([]MessageResult, len(messages))}
	for i, msg := range messages {
		result.Messages[i] = messageToResult(msg)
	}
	summarizeTurn(&result)
	// Prefer the model actually recorded on the final assistant message (it
	// reflects overrides/routing for this turn); the registry default is
	// only a fallback for the rare case a message predates that column.
	result.Model = h.mgr.DefaultModel()
	for i := len(result.Messages) - 1; i >= 0; i-- {
		if result.Messages[i].Role == "assistant" && result.Messages[i].Model != "" {
			result.Model = result.Messages[i].Model
			break
		}
	}
	return h.resultResponse(req.ID, result)
}

// summarizeTurn fills the additive summary fields of an ExecuteTurnResult
// from its Messages transcript: the final assistant content, the token usage
// recorded on that message, and a per-call tool trace. Tool call success is
// inferred from the agent loop convention of prefixing failed tool results
// with "ERROR: ".
func summarizeTurn(result *ExecuteTurnResult) {
	var lastAssistant *MessageResult
	toolResults := make(map[string]string) // tool_call_id -> result content
	for i := range result.Messages {
		msg := &result.Messages[i]
		switch msg.Role {
		case "assistant":
			lastAssistant = msg
			for _, tc := range msg.ToolCalls {
				var args json.RawMessage
				if tc.Function.Arguments != "" {
					if parsed := json.RawMessage(tc.Function.Arguments); json.Valid(parsed) {
						args = parsed
					}
				}
				result.ToolTrace = append(result.ToolTrace, ToolTraceResult{
					Name: tc.Function.Name,
					Args: args,
				})
			}
		case "tool":
			if msg.ToolCallID != "" {
				toolResults[msg.ToolCallID] = msg.Content
			}
		}
	}

	// Mark each trace entry OK/failed by matching the tool result content.
	// A call with no recorded result (never executed) is NOT ok — the old
	// default marked it successful, hiding streaming-fragment calls.
	traceIdx := 0
	for i := range result.Messages {
		if result.Messages[i].Role != "assistant" {
			continue
		}
		for _, tc := range result.Messages[i].ToolCalls {
			if traceIdx < len(result.ToolTrace) {
				content, found := toolResults[tc.ID]
				result.ToolTrace[traceIdx].OK = found && !strings.HasPrefix(content, "ERROR:")
				traceIdx++
			}
		}
	}

	if lastAssistant != nil {
		result.FinalContent = lastAssistant.Content
		if lastAssistant.Usage != nil {
			usage := *lastAssistant.Usage
			result.Usage = &usage
		}
	}
}

func (h *Handler) handleGetMessages(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params GetMessagesParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Limit <= 0 {
		params.Limit = 100
	}

	messages, err := h.mgr.GetMessages(ctx, params.SessionID, params.Limit, params.Offset)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "get messages failed", err.Error())
	}

	result := GetMessagesResult{Messages: make([]MessageResult, len(messages))}
	for i, msg := range messages {
		result.Messages[i] = messageToResult(msg)
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleGetMessagesSince(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params GetMessagesSinceParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	messages, err := h.mgr.GetMessagesSince(ctx, params.SessionID, params.SinceSeq)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "get messages since failed", err.Error())
	}

	result := GetMessagesResult{Messages: make([]MessageResult, len(messages))}
	for i, msg := range messages {
		result.Messages[i] = messageToResult(msg)
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleHaltSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params HaltSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Reason == "" {
		params.Reason = "user"
	}

	if err := h.mgr.HaltSession(params.SessionID, params.Reason); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "halt session failed", err.Error())
	}

	return h.resultResponse(req.ID, map[string]any{"halted": true, "session_id": params.SessionID})
}

func (h *Handler) handleResumeSession(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ResumeSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	if err := h.mgr.ResumeSession(params.SessionID); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "resume session failed", err.Error())
	}

	return h.resultResponse(req.ID, map[string]any{"resumed": true, "session_id": params.SessionID})
}

func (h *Handler) handleHaltAll(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	h.mgr.emergency.HaltAll("emergency")
	return h.resultResponse(req.ID, map[string]any{"halted_all": true})
}

func (h *Handler) handleStatus(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	sessions, _ := h.mgr.ListSessions(ctx, 1, 0)
	result := StatusResult{
		Running:  true,
		Sessions: len(sessions),
		Addr:     "", // filled by daemon
		Version:  "0.0.0-dev",
	}
	return h.resultResponse(req.ID, result)
}

func (h *Handler) handleSwitchModel(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params SwitchModelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SessionID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "session_id is required", nil)
	}
	if params.Model == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "model is required", nil)
	}

	if err := h.mgr.SwitchModel(ctx, params.SessionID, params.Model); err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		case errors.As(err, new(*ModelUnavailableError)):
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "model unavailable", err.Error())
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, "switch model failed", err.Error())
		}
	}

	return h.resultResponse(req.ID, map[string]any{"session_id": params.SessionID, "model": params.Model})
}

func (h *Handler) handleProviderList(_ context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	providers, err := h.mgr.ListProviders()
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "list providers failed", err.Error())
	}
	out := make([]ProviderResult, len(providers))
	for i, p := range providers {
		out[i] = ProviderResult{Name: p.Name, Kind: p.Kind}
	}
	return h.resultResponse(req.ID, ProviderListResult{Providers: out})
}

func (h *Handler) handleProviderListModels(_ context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ProviderListModelsParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Provider == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "provider is required", nil)
	}
	models, err := h.mgr.ListProviderModels(params.Provider)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "list provider models failed", err.Error())
	}
	return h.resultResponse(req.ID, ProviderListModelsResult{Provider: params.Provider, Models: models})
}

func (h *Handler) handleProviderSwitch(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ProviderSwitchParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	// Unlike session.switch_model, session_id is OPTIONAL here: a caller
	// with no session in play at all (forge daemon set-provider) still
	// switches the daemon's default provider+model — it just skips
	// recording the choice into any particular session's metadata.
	if params.Provider == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "provider is required", nil)
	}
	if params.Model == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "model is required (use provider.list_models to discover one first)", nil)
	}

	spec := params.Provider + "/" + params.Model
	if err := h.mgr.SwitchModel(ctx, params.SessionID, spec); err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		case errors.As(err, new(*ModelUnavailableError)):
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, "model unavailable", err.Error())
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, "switch provider failed", err.Error())
		}
	}
	return h.resultResponse(req.ID, map[string]any{"session_id": params.SessionID, "provider": params.Provider, "model": params.Model})
}

func (h *Handler) handleMarkSuccess(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params SessionMarkSuccessParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.SessionID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "session_id is required", nil)
	}
	if err := h.mgr.MarkSuccess(ctx, params.SessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "session not found", nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, "mark success failed", err.Error())
	}
	return h.resultResponse(req.ID, map[string]any{"marked": true, "session_id": params.SessionID})
}

func (h *Handler) handlePluginList(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.pluginMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "plugin manager not available", nil)
	}
	infos := h.pluginMgr.Info()
	out := make([]PluginInfoResult, 0, len(infos))
	for _, info := range infos {
		out = append(out, PluginInfoResult{
			Name:      info.Name,
			Version:   info.Version,
			Source:    info.Source,
			Enabled:   info.Enabled,
			ToolCount: info.ToolCount,
		})
	}
	return h.resultResponse(req.ID, PluginListResult{Plugins: out})
}

func (h *Handler) handlePluginEnable(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.pluginMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "plugin manager not available", nil)
	}
	var params PluginEnableParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Name == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "name is required", nil)
	}
	if err := h.pluginMgr.Enable(params.Name); err != nil {
		switch {
		case errors.Is(err, pluginwasm.ErrNotLoaded):
			return NewErrorResponse(req.ID, ErrCodeNotLoaded, err.Error(), nil)
		case errors.Is(err, pluginwasm.ErrAlreadyEnabled):
			return NewErrorResponse(req.ID, ErrCodeAlreadyEnabled, err.Error(), nil)
		case errors.Is(err, pluginwasm.ErrApprovalRequired):
			return NewErrorResponse(req.ID, ErrCodeApprovalRequired, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}
	return h.resultResponse(req.ID, map[string]any{"enabled": true, "name": params.Name})
}

func (h *Handler) handlePluginDisable(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.pluginMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "plugin manager not available", nil)
	}
	var params PluginDisableParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Name == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "name is required", nil)
	}
	if err := h.pluginMgr.Disable(params.Name); err != nil {
		switch {
		case errors.Is(err, pluginwasm.ErrNotLoaded):
			return NewErrorResponse(req.ID, ErrCodeNotLoaded, err.Error(), nil)
		case errors.Is(err, pluginwasm.ErrNotEnabled):
			return NewErrorResponse(req.ID, ErrCodeNotEnabled, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}
	return h.resultResponse(req.ID, map[string]any{"disabled": true, "name": params.Name})
}

func (h *Handler) handlePluginReload(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.pluginMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "plugin manager not available", nil)
	}
	results, err := h.pluginMgr.Reload()
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "plugin reload failed", err.Error())
	}
	out := make([]LoadResultEntry, 0, len(results))
	for _, r := range results {
		e := LoadResultEntry{Name: r.Name, Loaded: r.Loaded}
		if r.Err != nil {
			e.Error = r.Err.Error()
		}
		out = append(out, e)
	}
	if out == nil {
		out = []LoadResultEntry{}
	}
	return h.resultResponse(req.ID, PluginReloadResult{Results: out})
}

func (h *Handler) handleSkillList(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.skillMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "skill manager not available", nil)
	}
	infos := h.skillMgr.Info()
	out := make([]SkillInfoResult, 0, len(infos))
	for _, info := range infos {
		out = append(out, SkillInfoResult{
			Name:        info.Name,
			Description: info.Description,
			Category:    info.Category,
			Source:      info.Source,
			Enabled:     info.Enabled,
		})
	}
	return h.resultResponse(req.ID, SkillListResult{Skills: out})
}

func (h *Handler) handleSkillEnable(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.skillMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "skill manager not available", nil)
	}
	var params SkillEnableParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Name == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "name is required", nil)
	}
	if err := h.skillMgr.Enable(params.Name); err != nil {
		switch {
		case errors.Is(err, skill.ErrNotLoaded):
			return NewErrorResponse(req.ID, ErrCodeNotLoaded, err.Error(), nil)
		case errors.Is(err, skill.ErrAlreadyEnabled):
			return NewErrorResponse(req.ID, ErrCodeAlreadyEnabled, err.Error(), nil)
		case errors.Is(err, skill.ErrApprovalRequired):
			return NewErrorResponse(req.ID, ErrCodeApprovalRequired, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}
	return h.resultResponse(req.ID, map[string]any{"enabled": true, "name": params.Name})
}

func (h *Handler) handleSkillDisable(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.skillMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "skill manager not available", nil)
	}
	var params SkillDisableParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Name == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "name is required", nil)
	}
	if err := h.skillMgr.Disable(params.Name); err != nil {
		switch {
		case errors.Is(err, skill.ErrNotLoaded):
			return NewErrorResponse(req.ID, ErrCodeNotLoaded, err.Error(), nil)
		case errors.Is(err, skill.ErrNotEnabled):
			return NewErrorResponse(req.ID, ErrCodeNotEnabled, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}
	return h.resultResponse(req.ID, map[string]any{"disabled": true, "name": params.Name})
}

func (h *Handler) handleSkillReload(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	if h.skillMgr == nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "skill manager not available", nil)
	}
	results, err := h.skillMgr.Reload()
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeInternalError, "skill reload failed", err.Error())
	}
	out := make([]LoadResultEntry, 0, len(results))
	for _, r := range results {
		e := LoadResultEntry{Name: r.Name, Loaded: r.Loaded}
		if r.Err != nil {
			e.Error = r.Err.Error()
		}
		out = append(out, e)
	}
	if out == nil {
		out = []LoadResultEntry{}
	}
	return h.resultResponse(req.ID, SkillReloadResult{Results: out})
}

func (h *Handler) handleJobList(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	jobs := h.mgr.ListJobs()
	if jobs == nil {
		jobs = []JobResult{}
	}
	return h.resultResponse(req.ID, JobListResult{Jobs: jobs})
}

func (h *Handler) handleJobGet(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params JobGetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.JobID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "job_id is required", nil)
	}
	job, ok := h.mgr.GetJob(params.JobID)
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeJobNotFound, "job not found", nil)
	}
	return h.resultResponse(req.ID, job)
}

func (h *Handler) handleJobCancel(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params JobCancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.JobID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "job_id is required", nil)
	}
	job, err := h.mgr.CancelJob(params.JobID)
	if err != nil {
		return NewErrorResponse(req.ID, ErrCodeJobNotFound, "job not found", nil)
	}
	return h.resultResponse(req.ID, JobCancelResult{Canceled: job.Status == JobCanceled, JobID: job.ID, Status: job.Status})
}

// run.* handlers (RF-11 daemon migration, hojaDeRuta-multiagente.md Fase
// 3) — thin RPC wrappers over the engine SessionManager.StartRun/
// ResumeRun/GetRun/ListRuns/CancelRun/ApproveRunCheckpoint already
// implement (internal/daemon/runs.go, Fase 1/2). Manifest travels as
// content in the request, never a path — the daemon has no reason to read
// the client's filesystem.

func (h *Handler) handleRunStart(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params RunStartParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	stateDir := params.StateDir
	if stateDir == "" {
		stateDir = "."
	}
	exec, err := h.mgr.StartRun(ctx, &params.Manifest, stateDir, params.Decompose)
	if err != nil {
		if errors.Is(err, ErrRunAlreadyActive) {
			return NewErrorResponse(req.ID, ErrCodeRunAlreadyActive, err.Error(), nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
	}
	return h.resultResponse(req.ID, exec.snapshot())
}

func (h *Handler) handleRunStatus(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params RunStatusParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.RunID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "run_id is required", nil)
	}
	res, ok := h.mgr.GetRun(params.RunID)
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeRunNotFound, "run not found", nil)
	}
	return h.resultResponse(req.ID, res)
}

func (h *Handler) handleRunList(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	runs := h.mgr.ListRuns()
	if runs == nil {
		runs = []RunResult{}
	}
	return h.resultResponse(req.ID, RunListResult{Runs: runs})
}

func (h *Handler) handleRunResume(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params RunResumeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	stateDir := params.StateDir
	if stateDir == "" {
		stateDir = "."
	}
	exec, err := h.mgr.ResumeRun(ctx, &params.Manifest, stateDir)
	if err != nil {
		if errors.Is(err, ErrRunAlreadyActive) {
			return NewErrorResponse(req.ID, ErrCodeRunAlreadyActive, err.Error(), nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
	}
	return h.resultResponse(req.ID, exec.snapshot())
}

func (h *Handler) handleRunCancel(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params RunCancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.RunID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "run_id is required", nil)
	}
	res, err := h.mgr.CancelRun(params.RunID)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			return NewErrorResponse(req.ID, ErrCodeRunNotFound, "run not found", nil)
		}
		return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
	}
	return h.resultResponse(req.ID, res)
}

func (h *Handler) handleRunApproveCheckpoint(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params RunApproveCheckpointParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.RunID == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "run_id is required", nil)
	}
	res, err := h.mgr.ApproveRunCheckpoint(params.RunID, params.Approved)
	if err != nil {
		switch {
		case errors.Is(err, ErrRunNotFound):
			return NewErrorResponse(req.ID, ErrCodeRunNotFound, "run not found", nil)
		case errors.Is(err, ErrRunNoCheckpointPending):
			return NewErrorResponse(req.ID, ErrCodeRunNoCheckpointPending, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, err.Error(), nil)
		}
	}
	return h.resultResponse(req.ID, res)
}

func messageToResult(msg store.Message) MessageResult {
	var toolCalls []ToolCallResult
	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, ToolCallResult{
			ID:   tc.ID,
			Type: tc.Type,
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	var usage *UsageResult
	if msg.Usage != nil {
		usage = &UsageResult{
			PromptTokens:     msg.Usage.PromptTokens,
			CompletionTokens: msg.Usage.CompletionTokens,
			TotalTokens:      msg.Usage.TotalTokens,
		}
	}

	return MessageResult{
		ID:         msg.ID,
		Seq:        msg.Seq,
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCalls:  toolCalls,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
		Usage:      usage,
		Model:      msg.Model,
		DurationMs: msg.DurationMs,
		CreatedAt:  msg.CreatedAt,
	}
}

func (h *Handler) resultResponse(id *json.RawMessage, result any) *JSONRPCResponse {
	resp, err := NewResultResponse(id, result)
	if err != nil {
		return NewErrorResponse(id, ErrCodeInternalError, "marshal result failed", err.Error())
	}
	return resp
}
