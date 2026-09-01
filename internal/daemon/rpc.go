// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"encoding/json"
)

// JSONRPCRequest represents a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"` // "2.0"
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *JSONRPCError    `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSONRPCNotification represents a JSON-RPC 2.0 notification (no ID).
type JSONRPCNotification struct {
	JSONRPC string          `json:"jsonrpc"` // "2.0"
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError      = -32700
	ErrCodeInvalidRequest  = -32600
	ErrCodeMethodNotFound  = -32601
	ErrCodeInvalidParams   = -32602
	ErrCodeInternalError   = -32603
	ErrCodeSessionNotFound = -32001
	ErrCodeSessionHalted   = -32002
	ErrCodeToolError       = -32003
	ErrCodeNotLoaded       = -32010
	ErrCodeAlreadyEnabled  = -32011
	ErrCodeNotEnabled      = -32012
	ErrCodeApprovalRequired = -32013
	ErrCodeAlreadyExists   = -32014
)

// Method names for daemon -> client notifications.
const (
	MethodSessionEvent  = "session.event"   // session created/updated/deleted
	MethodMessageEvent  = "message.event"   // new message appended
	MethodToolCallEvent = "tool.call.event" // tool call started/finished
	MethodEmergencyHalt = "emergency.halt"  // emergency stop broadcast
)

// SessionEventPayload carries session lifecycle events.
type SessionEventPayload struct {
	Action    string         `json:"action"` // "created" | "updated" | "deleted"
	SessionID string         `json:"session_id"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// MessageEventPayload carries new message events.
type MessageEventPayload struct {
	SessionID string `json:"session_id"`
	Message   struct {
		ID        int64  `json:"id"`
		Seq       int    `json:"seq"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		CreatedAt int64  `json:"created_at"`
	} `json:"message"`
}

// ToolCallEventPayload carries tool call lifecycle events.
type ToolCallEventPayload struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Status     string `json:"status"` // "started" | "finished" | "error"
	Error      string `json:"error,omitempty"`
}

// EmergencyHaltPayload carries emergency halt notifications.
type EmergencyHaltPayload struct {
	SessionID string `json:"session_id,omitempty"` // empty = global halt
	Reason    string `json:"reason"`               // "user" | "emergency" | "budget"
}

// RPC method names (client -> daemon requests).
const (
	MethodCreateSession    = "session.create"
	MethodGetSession       = "session.get"
	MethodListSessions     = "session.list"
	MethodDeleteSession    = "session.delete"
	MethodExecuteTurn      = "session.execute_turn"
	MethodGetMessages      = "session.get_messages"
	MethodGetMessagesSince = "session.get_messages_since"
	MethodHaltSession      = "session.halt"
	MethodResumeSession    = "session.resume"
	MethodHaltAll          = "emergency.halt_all"
	MethodStatus           = "daemon.status"
	MethodSwitchModel      = "session.switch_model"
	MethodPluginList       = "plugin.list"
	MethodPluginEnable     = "plugin.enable"
	MethodPluginDisable    = "plugin.disable"
	MethodPluginReload     = "plugin.reload"
	MethodSkillList        = "skill.list"
	MethodSkillEnable      = "skill.enable"
	MethodSkillDisable     = "skill.disable"
	MethodSkillReload      = "skill.reload"
)

// CreateSessionParams for session.create.
type CreateSessionParams struct {
	Metadata map[string]any `json:"metadata,omitempty"`
}

// GetSessionParams for session.get.
type GetSessionParams struct {
	SessionID string `json:"session_id"`
}

// ListSessionsParams for session.list.
type ListSessionsParams struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// DeleteSessionParams for session.delete.
type DeleteSessionParams struct {
	SessionID string `json:"session_id"`
}

// ExecuteTurnParams for session.execute_turn.
type ExecuteTurnParams struct {
	SessionID        string `json:"session_id"`
	UserMessage      string `json:"user_message"`
	EnableRetrieval  bool   `json:"enable_retrieval,omitempty"`
	EnableCompaction bool   `json:"enable_compaction,omitempty"`
	EnableAnchoring  bool   `json:"enable_anchoring,omitempty"`
	EnableRouting    bool   `json:"enable_routing,omitempty"`
	EnableSkills     bool   `json:"enable_skills,omitempty"`
}

// GetMessagesParams for session.get_messages.
type GetMessagesParams struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

// GetMessagesSinceParams for session.get_messages_since.
type GetMessagesSinceParams struct {
	SessionID string `json:"session_id"`
	SinceSeq  int    `json:"since_seq"`
}

// HaltSessionParams for session.halt.
type HaltSessionParams struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason,omitempty"` // "user" | "emergency" | "budget"
}

// ResumeSessionParams for session.resume.
type ResumeSessionParams struct {
	SessionID string `json:"session_id"`
}

// SwitchModelParams for session.switch_model.
type SwitchModelParams struct {
	SessionID string `json:"session_id"` // session whose metadata records the choice
	Model     string `json:"model"`
}

// PluginEnableParams for plugin.enable / plugin.disable.
type PluginEnableParams struct {
	Name string `json:"name"`
}

// PluginDisableParams for plugin.disable.
type PluginDisableParams struct {
	Name string `json:"name"`
}

// PluginInfoResult for plugin.list.
type PluginInfoResult struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	ToolCount int    `json:"tool_count"`
}

// PluginListResult for plugin.list.
type PluginListResult struct {
	Plugins []PluginInfoResult `json:"plugins"`
}

// PluginReloadResult for plugin.reload.
type PluginReloadResult struct {
	Results []LoadResultEntry `json:"results"`
}

// SkillEnableParams for skill.enable / skill.disable.
type SkillEnableParams struct {
	Name string `json:"name"`
}

// SkillDisableParams for skill.disable.
type SkillDisableParams struct {
	Name string `json:"name"`
}

// SkillInfoResult for skill.list.
type SkillInfoResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
}

// SkillListResult for skill.list.
type SkillListResult struct {
	Skills []SkillInfoResult `json:"skills"`
}

// SkillReloadResult for skill.reload.
type SkillReloadResult struct {
	Results []LoadResultEntry `json:"results"`
}

// LoadResultEntry is the JSON form of a LoadResult.
type LoadResultEntry struct {
	Name   string `json:"name"`
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
}

// SessionResult for session operations.
type SessionResult struct {
	ID           string         `json:"id"`
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	MessageCount int            `json:"message_count,omitempty"`
}

// ListSessionsResult for session.list.
type ListSessionsResult struct {
	Sessions []SessionResult `json:"sessions"`
}

// ExecuteTurnResult for session.execute_turn.
//
// The summary fields (FinalContent, Model, ToolTrace, Usage) let clients build
// a complete turn report without extra round-trips. They are additive and
// backward compatible within the package: Messages remains the authoritative
// full transcript of the turn.
type ExecuteTurnResult struct {
	Messages     []MessageResult   `json:"messages"`
	FinalContent string            `json:"final_content,omitempty"` // last assistant message content
	Model        string            `json:"model,omitempty"`         // default model used for the turn
	ToolTrace    []ToolTraceResult `json:"tool_trace,omitempty"`
	Usage        *UsageResult      `json:"usage,omitempty"` // usage recorded on the final assistant message
}

// ToolTraceResult summarizes one tool call executed during a turn.
type ToolTraceResult struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"` // parsed from the assistant's arguments JSON
	OK   bool            `json:"ok"`
}

// MessageResult represents a message in RPC responses.
type MessageResult struct {
	ID         int64            `json:"id"`
	Seq        int              `json:"seq"`
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []ToolCallResult `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
	Usage      *UsageResult     `json:"usage,omitempty"`
	CreatedAt  int64            `json:"created_at"`
}

// ToolCallResult represents a tool call in RPC responses.
type ToolCallResult struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// UsageResult represents token usage in RPC responses.
type UsageResult struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// GetMessagesResult for session.get_messages.
type GetMessagesResult struct {
	Messages []MessageResult `json:"messages"`
}

// StatusResult for daemon.status.
type StatusResult struct {
	Running  bool   `json:"running"`
	Sessions int    `json:"sessions"`
	Addr     string `json:"addr"`
	Version  string `json:"version"`
}

// NewErrorResponse creates a JSONRPCResponse with an error.
func NewErrorResponse(id *json.RawMessage, code int, message string, data any) *JSONRPCResponse {
	err := &JSONRPCError{Code: code, Message: message}
	if data != nil {
		err.Data = data
	}
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   err,
	}
}

// NewResultResponse creates a JSONRPCResponse with a result.
func NewResultResponse(id *json.RawMessage, result any) (*JSONRPCResponse, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  data,
	}, nil
}

// NewNotification creates a JSONRPCNotification.
func NewNotification(method string, params any) (*JSONRPCNotification, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  data,
	}, nil
}
