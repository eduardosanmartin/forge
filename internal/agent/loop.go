// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/routing"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// StoreInterface defines the store operations needed by Agent.
type StoreInterface interface {
	GetSession(ctx context.Context, id string) (store.Session, error)
	AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error)
	GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error)
	GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error)
}

// LLMRegistryInterface defines the LLM registry operations needed by Agent.
type LLMRegistryInterface interface {
	GetDefault() (llm.Provider, string)
	Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

// ToolsRegistryInterface defines the tools registry operations needed by Agent.
type ToolsRegistryInterface interface {
	List() []tools.Tool
	Execute(ctx context.Context, name string, args map[string]any) (tools.Result, error)
}

// PermsEngineInterface defines the permission engine operations needed by Agent.
type PermsEngineInterface interface {
	Check(req perms.Request) perms.Decision
}

// ModelForStepSelector matches LLM registries that can resolve a model per
// routing step type (llm.Registry implements it through its ModelRouter,
// which is fed from config providers.<name>.model_roles).
type ModelForStepSelector interface {
	GetModelForStep(step routing.StepType) string
}

// TurnOptions controls optional per-turn behavior (additive, backward compatible).
//
// StreamingEnabled enables the streaming path (ChatStream) when true. When false
// (default) or when the provider returns ErrStreamingNotSupported, the turn uses
// the canonical Chat path with identical semantics. Mid-stream failures fail the
// turn predictably; the next turn may run non-streaming if streaming is disabled.
// OnDelta is called for each text delta when streaming; it is ignored when
// streaming is disabled. The callback must be non-blocking; the agent does not
// enforce ordering beyond sequential delta emission.
// Timeout overrides the configured agent.max_turn_seconds for this turn when
// positive; zero/negative keeps the configured default.
type TurnOptions struct {
	StreamingEnabled bool
	OnDelta          func(delta string)
	Timeout          time.Duration
}

// ChatStreamer matches providers/registries that support streaming.
type ChatStreamer interface {
	ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error)
}

// Agent orchestrates the agent loop: user message -> assistant -> tool calls -> ... -> final answer.
type Agent struct {
	cfg           *config.Config
	ctxAssembler  *ContextAssembler
	llmReg        LLMRegistryInterface
	toolsReg      ToolsRegistryInterface
	permsEngine   PermsEngineInterface
	store         StoreInterface
	logger        *slog.Logger
	maxIterations int
	maxTurnSeconds int
}

// NewAgent creates a new Agent from configuration and dependencies.
func NewAgent(
	cfg *config.Config,
	store StoreInterface,
	llmReg LLMRegistryInterface,
	toolsReg ToolsRegistryInterface,
	permsEngine PermsEngineInterface,
	logger *slog.Logger,
) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	maxIterations := 10
	if cfg != nil && cfg.Agent.MaxIterations > 0 {
		maxIterations = cfg.Agent.MaxIterations
	}
	maxTurnSeconds := config.DefaultAgentMaxTurnSeconds
	if cfg != nil && cfg.Agent.MaxTurnSeconds > 0 {
		maxTurnSeconds = cfg.Agent.MaxTurnSeconds
	}
	return &Agent{
		cfg:           cfg,
		ctxAssembler:  NewContextAssembler(toolsReg, store, 8), // default 8 turns history (RNF-10-tuned, see bench)
		llmReg:        llmReg,
		toolsReg:      toolsReg,
		permsEngine:   permsEngine,
		store:         store,
		logger:        logger,
		maxIterations: maxIterations,
		maxTurnSeconds: maxTurnSeconds,
	}
}

// SetV1Deps wires the optional v1 feature dependencies into the agent's
// context assembler. Intended to be called once at construction time.
func (a *Agent) SetV1Deps(deps V1Deps) {
	a.ctxAssembler.SetV1Deps(deps)
}

// timeoutError reports a turn timeout with the actionable config pointer,
// mirroring the max_iterations message style. A non-deadline ctx error
// (e.g. caller cancellation) is reported as-is.
func (a *Agent) timeoutError(ctx context.Context, timeout time.Duration) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("turn aborted: turn timeout after %s — raise agent.max_turn_seconds in .forge/config.json", timeout)
	}
	return fmt.Errorf("turn aborted: %w", ctx.Err())
}

// ExecuteTurn runs one complete turn: user message -> assistant -> tool calls -> ... -> final answer.
// It honors config llm.streaming (default OFF) for backward compatibility.
func (a *Agent) ExecuteTurn(ctx context.Context, sessionID string, userMessage string) (TurnResult, error) {
	enabled := a.cfg != nil && a.cfg.LLM.Streaming
	return a.ExecuteTurnWithOptions(ctx, sessionID, userMessage, TurnOptions{StreamingEnabled: enabled})
}

// ExecuteTurnWithOptions runs one turn with explicit per-turn options (additive).
func (a *Agent) ExecuteTurnWithOptions(ctx context.Context, sessionID string, userMessage string, opts TurnOptions) (TurnResult, error) {
	startTime := time.Now()
	result := TurnResult{
		Metrics: TurnMetrics{
			StartTime: startTime,
		},
	}

	// Turn timeout: the whole turn (store, LLM, tools) is bounded so a hung
	// provider fails visibly instead of locking the caller forever. A
	// positive per-turn override wins over the configured default.
	timeout := time.Duration(a.maxTurnSeconds) * time.Second
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Check if session is halted via metadata
	session, err := a.store.GetSession(ctx, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("session not found: %w", err)
		result.Halted = true
		result.Metrics.EndTime = time.Now()
		return result, result.Error
	}

	if halted, _ := session.Metadata["halted"].(bool); halted {
		reason, _ := session.Metadata["halt_reason"].(string)
		result.Error = fmt.Errorf("session halted: %s", reason)
		result.Halted = true
		result.Metrics.EndTime = time.Now()
		return result, result.Error
	}

	// v1 routing flag (session metadata, persisted by the SessionManager
	// before the turn starts): when on, the main generation call resolves
	// its model through the registry's router.
	routingEnabled, _ := session.Metadata["v1_routing"].(bool)

	// 1. Persist user message to store (role="user")
	userMsg := &store.Message{
		SessionID: sessionID,
		Role:      "user",
		Content:   userMessage,
	}
	_, _, err = a.store.AppendMessage(ctx, userMsg)
	if err != nil {
		result.Error = fmt.Errorf("append user message: %w", err)
		result.Halted = true
		result.Metrics.EndTime = time.Now()
		return result, result.Error
	}
	result.Messages = append(result.Messages, *userMsg)

	iterationCount := 0
	var totalLLMTimeMs int64
	var totalPromptTokens, totalCompletionTokens, totalTokens int
	var totalToolCallCount int

	for {
		iterationCount++
		if iterationCount > a.maxIterations {
			result.Error = fmt.Errorf("turn aborted: agent reached max_iterations (%d) after %d iterations — raise agent.max_iterations in .forge/config.json", a.maxIterations, iterationCount-1)
			result.Halted = true
			break
		}
		// Belt-and-suspenders alongside ctx cancellation (which unblocks
		// in-flight LLM/tool calls): fail fast without another round trip.
		if ctx.Err() != nil {
			result.Error = a.timeoutError(ctx, timeout)
			result.Halted = true
			break
		}

		// Build context via ContextAssembler
		llmMessages, err := a.ctxAssembler.Build(ctx, sessionID, userMessage)
		if err != nil {
			result.Error = fmt.Errorf("build context: %w", err)
			result.Halted = true
			break
		}

		// Get tool definitions
		toolDefs := a.ctxAssembler.ToolDefs()

		// Call LLM
		llmStartTime := time.Now()
		provider, model := a.llmReg.GetDefault()
		if provider == nil {
			result.Error = errors.New("no LLM provider available")
			result.Halted = true
			break
		}

		// v1 routing: when the session flag is on and the registry can
		// resolve step models, the main generation call uses the router's
		// model for the generate step (config providers.<name>.model_roles
		// -> generation role). In this build routing affects exactly this
		// existing operation: retrieval embeddings and compaction summaries
		// are deterministic and make no model calls, so there is nothing
		// else to route. The provider stays the default one; only the
		// model name changes, and an unresolvable role falls back to the
		// default model.
		if routingEnabled {
			if sel, ok := a.llmReg.(ModelForStepSelector); ok {
				if routed := sel.GetModelForStep(routing.StepGenerate); routed != "" {
					model = routed
				}
			}
		}

		req := llm.ChatRequest{
			Model:    model,
			Messages: llmMessages,
			Tools:    toolDefs,
			Stream:   false,
		}

		var resp llm.ChatResponse
		if opts.StreamingEnabled {
			resp, err = a.callLLMStream(ctx, provider, req, opts.OnDelta)
			if err != nil && errors.Is(err, llm.ErrStreamingNotSupported) {
				// Provider does not support streaming: exact Chat fallback (WU3).
				resp, err = provider.Chat(ctx, req)
			}
		} else {
			resp, err = provider.Chat(ctx, req)
		}
		llmElapsed := time.Since(llmStartTime).Milliseconds()
		totalLLMTimeMs += llmElapsed

		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				result.Error = a.timeoutError(ctx, timeout)
			} else {
				result.Error = fmt.Errorf("llm chat: %w", err)
			}
			result.Halted = true
			break
		}

		if len(resp.Choices) == 0 {
			result.Error = errors.New("no response from LLM")
			result.Halted = true
			break
		}

		choice := resp.Choices[0]

		// Track token usage
		if resp.Usage != nil {
			totalPromptTokens += resp.Usage.PromptTokens
			totalCompletionTokens += resp.Usage.CompletionTokens
			totalTokens += resp.Usage.TotalTokens
		}

		// Check if response has tool calls
		if len(choice.Message.ToolCalls) > 0 {
			// Append assistant message with tool calls
			assistantMsg := &store.Message{
				SessionID: sessionID,
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: choice.Message.ToolCalls,
				Usage:     resp.Usage,
			}
			_, _, err = a.store.AppendMessage(ctx, assistantMsg)
			if err != nil {
				result.Error = fmt.Errorf("append assistant message: %w", err)
				result.Halted = true
				break
			}
			result.Messages = append(result.Messages, *assistantMsg)

			// Execute each tool call
			for _, tc := range choice.Message.ToolCalls {
				// Check for halt during tool execution
				session, err := a.store.GetSession(ctx, sessionID)
				if err != nil {
					result.Error = fmt.Errorf("get session during tool execution: %w", err)
					result.Halted = true
					break
				}
				if halted, _ := session.Metadata["halted"].(bool); halted {
					reason, _ := session.Metadata["halt_reason"].(string)
					result.Error = fmt.Errorf("session halted during tool execution: %s", reason)
					result.Halted = true
					break
				}

				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = map[string]any{"_error": "invalid arguments: " + err.Error()}
				}

				// Execute tool (toolsReg.Execute handles perms check + execution + fencing + redaction)
				toolResult, err := a.toolsReg.Execute(ctx, tc.Function.Name, args)
				if err != nil {
					toolResult = tools.Result{
						Content: "ERROR: " + err.Error(),
					}
				}
				totalToolCallCount++

				// Append tool result message
				toolResultMsg := &store.Message{
					SessionID:  sessionID,
					Role:       "tool",
					Content:    toolResult.Content,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				}
				_, _, err = a.store.AppendMessage(ctx, toolResultMsg)
				if err != nil {
					result.Error = fmt.Errorf("append tool result: %w", err)
					result.Halted = true
					break
				}
				result.Messages = append(result.Messages, *toolResultMsg)
			}

			if result.Halted {
				break
			}

			// Loop back to continue with updated context (new messages included)
			// The userMessage for subsequent iterations should be empty since we're continuing
			// the conversation with tool results already in history
			userMessage = ""
			continue
		}

		// No tool calls - final response
		finalMsg := &store.Message{
			SessionID: sessionID,
			Role:      "assistant",
			Content:   choice.Message.Content,
			Usage:     resp.Usage,
		}
		_, _, err = a.store.AppendMessage(ctx, finalMsg)
		if err != nil {
			result.Error = fmt.Errorf("append final message: %w", err)
			result.Halted = true
			break
		}
		result.Messages = append(result.Messages, *finalMsg)

		// Success - populate metrics
		result.Metrics.EndTime = time.Now()
		result.Metrics.LLMTimeMs = totalLLMTimeMs
		result.Metrics.HarnessOverheadMs = result.Metrics.DurationMs() - totalLLMTimeMs
		result.Metrics.TotalTokens = totalTokens
		result.Metrics.PromptTokens = totalPromptTokens
		result.Metrics.CompletionTokens = totalCompletionTokens
		result.Metrics.ToolCallCount = totalToolCallCount
		result.Metrics.IterationCount = iterationCount

		return result, nil
	}

	// Error case - ensure metrics are populated
	result.Metrics.EndTime = time.Now()
	result.Metrics.LLMTimeMs = totalLLMTimeMs
	result.Metrics.HarnessOverheadMs = result.Metrics.DurationMs() - totalLLMTimeMs
	result.Metrics.TotalTokens = totalTokens
	result.Metrics.PromptTokens = totalPromptTokens
	result.Metrics.CompletionTokens = totalCompletionTokens
	result.Metrics.ToolCallCount = totalToolCallCount
	result.Metrics.IterationCount = iterationCount

	return result, result.Error
}

// callLLMStream attempts streaming and assembles a ChatResponse.
// On any mid-stream failure it returns an error and the caller must fail the turn
// (documented contract: no fallback within the same turn; the next turn may be non-streaming).
func (a *Agent) callLLMStream(ctx context.Context, provider llm.Provider, req llm.ChatRequest, onDelta func(string)) (llm.ChatResponse, error) {
	streamer, ok := provider.(ChatStreamer)
	if !ok {
		return llm.ChatResponse{}, fmt.Errorf("%w: provider does not implement ChatStream", llm.ErrStreamingNotSupported)
	}
	// Ensure request signals streaming for providers that inspect it.
	req.Stream = true
	ch, err := streamer.ChatStream(ctx, req)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	if ch == nil {
		return llm.ChatResponse{}, errors.New("nil stream channel")
	}
	content, toolCalls, usage, finishReason, cErr := consumeStream(ctx, ch, onDelta)
	if cErr != nil {
		return llm.ChatResponse{}, cErr
	}
	// Map to ChatResponse so the existing tool-execution loop is reused verbatim.
	return llm.ChatResponse{
		ID:    "stream-" + req.Model,
		Model: req.Model,
		Choices: []llm.Choice{{
			Index: 0,
			Message: llm.Message{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}, nil
}

// consumeStream assembles text and tool calls from a StreamChunk channel.
// It forwards each text delta to onDelta when non-nil.
// A chunk with Error != "" is treated as terminal mid-stream failure.
// mergeToolCallDelta folds one streamed tool-call fragment into the
// accumulated list. OpenAI-style streaming sends a call across chunks: the
// first carries id/name, follow-ups carry only argument slices with an empty
// name. Appending fragments verbatim executed half-calls (fs_list with no
// args → schema ERROR, plus an unnamed call → unknown-tool ERROR) so the
// real call never ran. An empty-name fragment continues the last open entry
// while its arguments are still incomplete JSON; a named fragment always
// starts a new entry (parallel calls and pre-assembled provider calls are
// never merged).
func mergeToolCallDelta(calls []llm.ToolCall, frag llm.ToolCall) []llm.ToolCall {
	if frag.Function.Name == "" && len(calls) > 0 {
		last := &calls[len(calls)-1]
		if last.Function.Name != "" && !json.Valid([]byte(last.Function.Arguments)) {
			last.Function.Arguments += frag.Function.Arguments
			if last.ID == "" {
				last.ID = frag.ID
			}
			if last.Type == "" {
				last.Type = frag.Type
			}
			return calls
		}
	}
	return append(calls, frag)
}

// consumeStream assembles text and tool calls from a StreamChunk channel.
// It forwards each text delta to onDelta when non-nil.
// A chunk with Error != "" is treated as terminal mid-stream failure.
// Context cancellation is respected and produces a context error.
func consumeStream(ctx context.Context, ch <-chan llm.StreamChunk, onDelta func(string)) (string, []llm.ToolCall, *llm.Usage, string, error) {
	var (
		contentBuilder strings.Builder
		toolCalls      []llm.ToolCall
		usage          *llm.Usage
		finishReason   string
	)
	for {
		select {
		case <-ctx.Done():
			return "", nil, nil, "", ctx.Err()
		case chunk, ok := <-ch:
			if !ok {
				// Channel closed: final assembly.
				if finishReason == "" {
					if len(toolCalls) > 0 {
						finishReason = "tool_calls"
					} else {
						finishReason = "stop"
					}
				}
				return contentBuilder.String(), toolCalls, usage, finishReason, nil
			}
			if chunk.Error != "" {
				return "", nil, nil, "", fmt.Errorf("stream error: %s", chunk.Error)
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					contentBuilder.WriteString(choice.Delta.Content)
					if onDelta != nil {
						onDelta(choice.Delta.Content)
					}
				}
				if len(choice.Delta.ToolCalls) > 0 {
					for _, frag := range choice.Delta.ToolCalls {
						toolCalls = mergeToolCallDelta(toolCalls, frag)
					}
				}
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					finishReason = *choice.FinishReason
				}
			}
			// Also consider chunk-level usage already captured.
		}
	}
}
