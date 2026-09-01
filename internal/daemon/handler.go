// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

func (h *Handler) handleExecuteTurn(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params ExecuteTurnParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}

	messages, err := h.mgr.ExecuteTurn(ctx, params.SessionID, params.UserMessage,
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
		result.Messages[i] = h.messageToResult(msg)
	}
	summarizeTurn(&result)
	result.Model = h.mgr.DefaultModel()
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
	traceIdx := 0
	for i := range result.Messages {
		if result.Messages[i].Role != "assistant" {
			continue
		}
		for _, tc := range result.Messages[i].ToolCalls {
			if traceIdx < len(result.ToolTrace) {
				content := toolResults[tc.ID]
				result.ToolTrace[traceIdx].OK = !strings.HasPrefix(content, "ERROR:")
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
		result.Messages[i] = h.messageToResult(msg)
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
		result.Messages[i] = h.messageToResult(msg)
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

func (h *Handler) messageToResult(msg store.Message) MessageResult {
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
