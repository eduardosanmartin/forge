// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/eduardosanmartin/forge/internal/store"
)

// Handler dispatches JSON-RPC requests to the session manager.
type Handler struct {
	mgr    *SessionManager
	logger *slog.Logger
}

// NewHandler creates a new Handler.
func NewHandler(mgr *SessionManager, logger *slog.Logger) *Handler {
	return &Handler{mgr: mgr, logger: logger}
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

	messages, err := h.mgr.ExecuteTurn(ctx, params.SessionID, params.UserMessage)
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
