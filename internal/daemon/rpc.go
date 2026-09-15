// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"encoding/json"

	"github.com/eduardosanmartin/forge/internal/cost"
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
	ErrCodeParseError       = -32700
	ErrCodeInvalidRequest   = -32600
	ErrCodeMethodNotFound   = -32601
	ErrCodeInvalidParams    = -32602
	ErrCodeInternalError    = -32603
	ErrCodeSessionNotFound  = -32001
	ErrCodeSessionHalted    = -32002
	ErrCodeToolError        = -32003
	ErrCodeNotLoaded        = -32010
	ErrCodeAlreadyEnabled   = -32011
	ErrCodeNotEnabled       = -32012
	ErrCodeApprovalRequired = -32013
	ErrCodeAlreadyExists    = -32014
	ErrCodeJobNotFound      = -32020
)

// Method names for daemon -> client notifications.
const (
	MethodSessionEvent  = "session.event"       // session created/updated/deleted
	MethodMessageEvent  = "message.event"       // new message appended
	MethodToolCallEvent = "tool.call.event"     // tool call started/finished
	MethodEmergencyHalt = "emergency.halt"      // emergency stop broadcast
	MethodMessageDelta  = "message.delta.event" // WU3: live text delta during streaming (additive)
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

// MessageDeltaPayload carries live streaming deltas (WU3).
// It is additive and does not alter existing message.event shapes consumed by TUI-2.
// TTFTMs is time-to-first-token in milliseconds, emitted only on the first delta of a streaming turn.
type MessageDeltaPayload struct {
	SessionID string `json:"session_id"`
	Delta     string `json:"delta"`
	Seq       *int   `json:"seq,omitempty"`
	TTFTMs    *int64 `json:"ttft_ms,omitempty"`
}

// EmergencyHaltPayload carries emergency halt notifications.
type EmergencyHaltPayload struct {
	SessionID string `json:"session_id,omitempty"` // empty = global halt
	Reason    string `json:"reason"`               // "user" | "emergency" | "budget"
}

// RPC method names (client -> daemon requests).
const (
	MethodCreateSession      = "session.create"
	MethodGetSession         = "session.get"
	MethodListSessions       = "session.list"
	MethodDeleteSession      = "session.delete"
	MethodBranchSession      = "session.branch"
	MethodMergeSession       = "session.merge"
	MethodExecuteTurn        = "session.execute_turn"
	MethodGetMessages        = "session.get_messages"
	MethodGetMessagesSince   = "session.get_messages_since"
	MethodHaltSession        = "session.halt"
	MethodResumeSession      = "session.resume"
	MethodHaltAll            = "emergency.halt_all"
	MethodStatus             = "daemon.status"
	MethodSwitchModel        = "session.switch_model"
	MethodSessionMarkSuccess = "session.mark_success"
	MethodCompareSessions    = "session.compare"
	MethodPluginList         = "plugin.list"
	MethodPluginEnable       = "plugin.enable"
	MethodPluginDisable      = "plugin.disable"
	MethodPluginReload       = "plugin.reload"
	MethodSkillList          = "skill.list"
	MethodSkillEnable        = "skill.enable"
	MethodSkillDisable       = "skill.disable"
	MethodSkillReload        = "skill.reload"
	MethodJobList            = "job.list"
	MethodJobGet             = "job.get"
	MethodJobCancel          = "job.cancel"
	// RF-3.4: manual inspect/edit of anchored facts (user-initiated).
	MethodMemoryList   = "memory.list"
	MethodMemoryGet    = "memory.get"
	MethodMemoryCreate = "memory.create"
	MethodMemoryUpdate = "memory.update"
	MethodMemoryDelete = "memory.delete"
	// RF-9.3: multi-model fanout.
	MethodFanout = "session.fanout"
	// RNF-6.3: estimated cost metrics.
	MethodSessionCost = "session.cost"
	MethodCostSummary = "cost.summary"
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
	// ModelHint pins this turn to a router role ("cheap"/"generation"/
	// "reasoning" — routing.ModelRole) instead of the session's default
	// model, resolved server-side via the registry's ModelRouter. Empty
	// (the default for every existing caller) behaves exactly as before.
	// Wired from run.Task.ModelHint for RF-11 manifest task execution
	// (client.ManifestExecutor) — see SessionManager.ExecuteTurnWithModelHint.
	ModelHint string `json:"model_hint,omitempty"`
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

// BranchSessionParams for session.branch.
type BranchSessionParams struct {
	SourceSessionID string         `json:"source_session_id"`
	AtSeq           int            `json:"at_seq,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// MergeSessionParams for session.merge.
type MergeSessionParams struct {
	SourceSessionID string `json:"source_session_id"`
	TargetSessionID string `json:"target_session_id"`
}

// SessionMarkSuccessParams for session.mark_success.
type SessionMarkSuccessParams struct {
	SessionID string `json:"session_id"`
}

// CompareSessionsParams for session.compare.
type CompareSessionsParams struct {
	SessionA string `json:"session_a"`
	SessionB string `json:"session_b"`
}

// CompareSessionsResult for session.compare.
type CompareSessionsResult struct {
	SessionA        SessionResult   `json:"session_a"`
	SessionB        SessionResult   `json:"session_b"`
	BranchAtSeqA    int             `json:"branch_at_seq_a"`
	BranchAtSeqB    int             `json:"branch_at_seq_b"`
	BranchParentA   string          `json:"branch_parent_a,omitempty"`
	BranchParentB   string          `json:"branch_parent_b,omitempty"`
	BranchRootA     string          `json:"branch_root_a,omitempty"`
	BranchRootB     string          `json:"branch_root_b,omitempty"`
	CountA          int             `json:"count_a"`
	CountB          int             `json:"count_b"`
	DivergentCountA int             `json:"divergent_count_a"`
	DivergentCountB int             `json:"divergent_count_b"`
	SameSession     bool            `json:"same_session"`
	DivergentA      []MessageResult `json:"divergent_a,omitempty"`
	DivergentB      []MessageResult `json:"divergent_b,omitempty"`
	LastMessageA    *MessageResult  `json:"last_message_a,omitempty"`
	LastMessageB    *MessageResult  `json:"last_message_b,omitempty"`
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
	Model      string           `json:"model,omitempty"`       // model that produced this message (assistant only)
	DurationMs int64            `json:"duration_ms,omitempty"` // LLM call time that produced this message (assistant only)
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

// JobResult for job operations.
type JobResult struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Error     string `json:"error,omitempty"`
	StartSeq  int    `json:"start_seq,omitempty"`
}

// JobListResult for job.list.
type JobListResult struct {
	Jobs []JobResult `json:"jobs"`
}

// JobGetParams for job.get.
type JobGetParams struct {
	JobID string `json:"job_id"`
}

// JobCancelParams for job.cancel.
type JobCancelParams struct {
	JobID string `json:"job_id"`
}

// JobCancelResult for job.cancel.
type JobCancelResult struct {
	Canceled bool   `json:"canceled"`
	JobID    string `json:"job_id"`
	Status   string `json:"status,omitempty"`
}

// AnchorResult is the wire form of a persisted anchor (RF-3.4).
type AnchorResult struct {
	ID        int64    `json:"id"`
	SessionID string   `json:"session_id"`
	Content   string   `json:"content"`
	Source    string   `json:"source"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// MemoryListParams for memory.list. An empty SessionID lists every anchor.
type MemoryListParams struct {
	SessionID string `json:"session_id,omitempty"`
}

// MemoryListResult for memory.list.
type MemoryListResult struct {
	Anchors []AnchorResult `json:"anchors"`
}

// MemoryGetParams for memory.get.
type MemoryGetParams struct {
	ID int64 `json:"id"`
}

// MemoryCreateParams for memory.create. Content is required. SessionID
// defaults to "global" when omitted (see handler_memory.go).
type MemoryCreateParams struct {
	Content   string   `json:"content"`
	SessionID string   `json:"session_id,omitempty"`
	Source    string   `json:"source,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

// MemoryUpdateParams for memory.update. Pointer fields distinguish "clear"
// from "leave untouched": nil means no change, a set value replaces it
// (nil Content/Source pointers keep the existing value; a non-nil
// Tags pointer including an empty slice clears the tags).
type MemoryUpdateParams struct {
	ID      int64     `json:"id"`
	Content *string   `json:"content,omitempty"`
	Source  *string   `json:"source,omitempty"`
	Tags    *[]string `json:"tags,omitempty"`
}

// MemoryResult for memory.create/update — the affected anchor.
type MemoryResult struct {
	Anchor AnchorResult `json:"anchor"`
}

// MemoryDeleteParams for memory.delete.
type MemoryDeleteParams struct {
	ID int64 `json:"id"`
}

// FanoutParams for session.fanout (RF-9.3).
//   - Task: the same user task executed on every child.
//   - Models: one entry per child, "provider/model" or bare "model" (bare
//     model entries use the daemon's default provider).
//   - SessionID: parent session; when empty a fresh labeled parent is created.
//   - MaxIterations/TokenBudget: optional passthrough to every ChildSpec.
type FanoutParams struct {
	Task          string   `json:"task"`
	Models        []string `json:"models"`
	SessionID     string   `json:"session_id,omitempty"`
	MaxIterations int      `json:"max_iter,omitempty"`
	TokenBudget   int      `json:"token_budget,omitempty"`
}

// FanoutChildResult summarizes one fanout child.
type FanoutChildResult struct {
	ChildSessionID string `json:"child_session_id"`
	Provider       string `json:"provider,omitempty"` // empty = default provider
	Model          string `json:"model"`              // resolved model name used for the child
	Success        bool   `json:"success"`
	Summary        string `json:"summary,omitempty"`
	Error          string `json:"error,omitempty"`
	// Lineage metadata for follow-up inspection via session.compare.
	BranchParent string `json:"branch_parent,omitempty"`
	BranchRoot   string `json:"branch_root,omitempty"`
	BranchAtSeq  int    `json:"branch_at_seq,omitempty"`
}

// FanoutResult for session.fanout.
type FanoutResult struct {
	ParentSessionID string              `json:"parent_session_id"`
	Children        []FanoutChildResult `json:"children"`
}

// SessionCostParams for session.cost (RNF-6.3).
type SessionCostParams struct {
	SessionID string `json:"session_id"`
}

// CostSummaryParams for cost.summary (RNF-6.3). Limit/Offset page through
// sessions the same way session.list does; 0 defaults like that method too.
type CostSummaryParams struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// CostSummaryResult for cost.summary.
type CostSummaryResult struct {
	Providers []cost.ProviderCost `json:"providers"`
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
