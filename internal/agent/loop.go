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

// chatFailover matches LLM registries that support automatic failover on a
// retryable failure (llm.Registry implements it via its config-driven
// fallback_chain — see llm.Registry.ChatWithFallback). Type-asserted rather
// than added to LLMRegistryInterface so a registry without failover support
// (test doubles, a future provider kind) just uses the plain resolved
// provider directly, unchanged. Only applied on the plain-default
// resolution path (see usingDefault in ExecuteTurnWithOptions) — an
// explicit override (spawn_subagent provider/model, a manifest task's
// model_hint, a routed step model) names one exact model on purpose, and
// silently substituting a different one on failure would ignore that
// choice instead of surfacing the error.
type chatFailover interface {
	ChatWithFallback(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

// fallbackChainSource matches LLM registries that expose the resolved
// fallback_chain (llm.Registry via llm.Registry.FallbackChain) for the
// streaming path to walk directly — see callLLMStreamWithFailover. Streaming
// can't use chatFailover/ChatWithFallback itself: that method only sees a
// synchronous request/response, but a streaming failure must be classified
// as pre-first-token (safe to retry) or mid-stream (already shown to the
// caller, must not retry) by consuming the channel, which only the caller
// of ChatStream can do.
type fallbackChainSource interface {
	FallbackChain() []llm.FallbackTarget
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
	// NoTools strips tool definitions from the ChatRequest for this turn,
	// forcing a plain-text answer. Used for turns that must never call a
	// tool no matter how capable/compliant the model is — e.g. the RF-11
	// manifest decomposition turn (internal/run/decompose.go), which asks
	// for a JSON task list and previously could exhaust max_iterations
	// exploring the filesystem instead of answering when the model chose to
	// use the tools it technically had available despite being told not to.
	NoTools bool
	// OverrideModel pins this single turn to a specific model name, taking
	// priority over the agent's own overrideModel (set only for spawn_subagent
	// children — see SpawnChild) and over the session's v1 routing flag.
	// Empty means "no override for this call" — the existing resolution
	// chain applies unchanged. Wired from a manifest Task.ModelHint resolved
	// through the registry's ModelRouter (internal/daemon/session_mgr.go's
	// ExecuteTurnWithModelHint) so per-task model sizing (RF-11 decomposition,
	// sugerenciasDeClaude.md §5.6) actually reaches the LLM call instead of
	// staying a purely declarative field.
	OverrideModel string
}

// ChatStreamer matches providers/registries that support streaming.
type ChatStreamer interface {
	ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error)
}

// Agent orchestrates the agent loop: user message -> assistant -> tool calls -> ... -> final answer.
type Agent struct {
	cfg            *config.Config
	ctxAssembler   *ContextAssembler
	llmReg         LLMRegistryInterface
	toolsReg       ToolsRegistryInterface
	permsEngine    PermsEngineInterface
	store          StoreInterface
	logger         *slog.Logger
	maxIterations  int
	maxTurnSeconds int
	// maxParallelChildren bounds concurrent child subagent turns (RF-1.2).
	// Worker pool size 2-4 (default 2) via agent.max_parallel_children.
	// Limit is documented here and enforced in scheduler.go + loop parallel
	// dispatch: LLM calls run in parallel on distinct branched sessions;
	// SQLite writes are serialized via single connection + WAL busy_timeout
	// (store/Store MaxOpenConns=1). Branched sessions never share a write txn.
	maxParallelChildren int
	// overrideProvider/overrideModel implement the RF-9.3 per-child
	// provider/model pinning (spawn_subagent + session.fanout). Zero values
	// mean "inherit the canonical default": the child keeps using the
	// default provider and whatever the routing flag resolves.
	overrideProvider llm.Provider
	overrideModel    string
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
	maxParallelChildren := config.DefaultAgentMaxParallelChildren
	if cfg != nil && cfg.Agent.MaxParallelChildren > 0 {
		maxParallelChildren = cfg.Agent.MaxParallelChildren
	}
	if maxParallelChildren < config.AgentMaxParallelChildrenMin {
		maxParallelChildren = config.AgentMaxParallelChildrenMin
	}
	if maxParallelChildren > config.AgentMaxParallelChildrenMax {
		maxParallelChildren = config.AgentMaxParallelChildrenMax
	}
	return &Agent{
		cfg:                 cfg,
		ctxAssembler:        NewContextAssembler(toolsReg, store, 8), // default 8 turns history (RNF-10-tuned, see bench)
		llmReg:              llmReg,
		toolsReg:            toolsReg,
		permsEngine:         permsEngine,
		store:               store,
		logger:              logger,
		maxIterations:       maxIterations,
		maxTurnSeconds:      maxTurnSeconds,
		maxParallelChildren: maxParallelChildren,
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
// It honors config llm.streaming.mode (default "off") for backward compatibility.
// Legacy bool `llm.streaming: true` maps to "on".
func (a *Agent) ExecuteTurn(ctx context.Context, sessionID string, userMessage string) (TurnResult, error) {
	enabled := a.cfg != nil && a.cfg.LLM.Streaming.IsEnabled()
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
	var firstTTFTMs int64
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

		// Get tool definitions — empty when this turn must never call a tool
		// (opts.NoTools), regardless of what the registry has available.
		var toolDefs []llm.ToolDef
		if !opts.NoTools {
			toolDefs = a.ctxAssembler.ToolDefs()
		}

		// Call LLM
		llmStartTime := time.Now()
		// RF-9.3: explicit per-child overrides win over everything else.
		provider, model := a.llmReg.GetDefault()
		// An overrideProvider replaces the default provider for every call
		// of this turn; an overrideModel replaces the model name. With a
		// model override the session routing flag does not apply — the
		// override is user-directed and routes nothing further. Overrides
		// are applied before the nil-provider check so a child pinned to a
		// named provider still runs when the registry has no usable default.
		// usingDefault tracks whether model resolution fell all the way
		// through to "the registry's plain default" with no override or
		// routing decision along the way — that's the ONLY case
		// ChatWithFallback applies to below. An explicit pin (spawn_subagent
		// provider/model, a manifest task's model_hint, or a routed step
		// model) is deliberate caller intent: it names one exact model, and
		// falling back to something else on failure would silently ignore
		// that choice instead of surfacing the error.
		usingDefault := true
		if a.overrideProvider != nil {
			provider = a.overrideProvider
			usingDefault = false
		}
		if provider == nil {
			result.Error = errors.New("no LLM provider available")
			result.Halted = true
			break
		}

		if opts.OverrideModel != "" {
			model = opts.OverrideModel
			usingDefault = false
		} else if a.overrideModel != "" {
			model = a.overrideModel
			usingDefault = false
		} else if routingEnabled {
			// v1 routing: when the session flag is on and the registry can
			// resolve step models, the main generation call uses the router's
			// model for the generate step (config providers.<name>.model_roles
			// -> generation role). In this build routing affects exactly this
			// existing operation: retrieval embeddings and compaction summaries
			// are deterministic and make no model calls, so there is nothing
			// else to route. The provider stays the default one; only the
			// model name changes, and an unresolvable role falls back to the
			// default model.
			if sel, ok := a.llmReg.(ModelForStepSelector); ok {
				if routed := sel.GetModelForStep(routing.StepGenerate); routed != "" {
					model = routed
					usingDefault = false
				}
			}
		}

		req := llm.ChatRequest{
			Model:     model,
			Messages:  llmMessages,
			Tools:     toolDefs,
			Stream:    false,
			SessionID: sessionID,
		}

		var resp llm.ChatResponse
		if opts.StreamingEnabled {
			var ttftMs int64
			if usingDefault {
				// Same failover eligibility as the non-streaming path
				// (usingDefault — see its comment above), but walked here
				// rather than via chatFailover: only callLLMStreamWithFailover
				// can tell a pre-first-token failure (safe to retry) from a
				// mid-stream one (already shown to the caller, must not retry).
				resp, ttftMs, err = a.callLLMStreamWithFailover(ctx, provider, model, req, opts.OnDelta, llmStartTime)
			} else {
				resp, ttftMs, err = a.callLLMStream(ctx, provider, req, opts.OnDelta, llmStartTime)
				if err != nil && errors.Is(err, llm.ErrStreamingNotSupported) {
					// Provider does not support streaming: exact Chat fallback (WU3).
					// Only this sentinel may fallback; mid-stream failures (stream error) must fail the turn.
					resp, err = provider.Chat(ctx, req)
					ttftMs = 0
				}
			}
			if ttftMs > 0 && firstTTFTMs == 0 {
				firstTTFTMs = ttftMs
				RecordTTFT(ttftMs)
				// Log TTFT for observability (best-effort).
				a.logger.Debug("stream ttft", "session_id", sessionID, "ttft_ms", ttftMs, "iteration", iterationCount)
			}
		} else if usingDefault {
			// Only the plain-default path gets failover — see usingDefault's
			// comment above. A registry without ChatWithFallback support
			// (e.g. a test double) falls through to the exact same call as
			// before this feature existed.
			if fo, ok := a.llmReg.(chatFailover); ok {
				resp, err = fo.ChatWithFallback(ctx, req)
			} else {
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
				SessionID:  sessionID,
				Role:       "assistant",
				Content:    choice.Message.Content,
				ToolCalls:  choice.Message.ToolCalls,
				Usage:      resp.Usage,
				Model:      model,
				DurationMs: llmElapsed,
			}
			_, _, err = a.store.AppendMessage(ctx, assistantMsg)
			if err != nil {
				result.Error = fmt.Errorf("append assistant message: %w", err)
				result.Halted = true
				break
			}
			result.Messages = append(result.Messages, *assistantMsg)

			// Execute each tool call — RF-1.2 parallel path:
			// When every call in this iteration is spawn_subagent and count >=2,
			// dispatch them through the bounded worker pool in scheduler.go
			// (agent.max_parallel_children, 2-4 default 2). Each child branches
			// to a distinct session so they do not share a SQLite write txn;
			// LLM calls run in parallel while DB appends serialize via single
			// SQLite connection + WAL busy_timeout. Parent tool-result appends
			// are serialized after join to avoid racing MAX(seq) on the parent
			// session. Mixed or non-spawn batches stay sequential
			// (file/session safety).
			allSpawn := len(choice.Message.ToolCalls) >= 2
			for _, tc := range choice.Message.ToolCalls {
				if tc.Function.Name != "spawn_subagent" {
					allSpawn = false
					break
				}
			}
			if allSpawn {
				// Single halt check before parallel fan-out.
				if sess, gErr := a.store.GetSession(ctx, sessionID); gErr == nil {
					if halted, _ := sess.Metadata["halted"].(bool); halted {
						reason, _ := sess.Metadata["halt_reason"].(string)
						result.Error = fmt.Errorf("session halted during tool execution: %s", reason)
						result.Halted = true
					}
				} else {
					result.Error = fmt.Errorf("get session during tool execution: %w", gErr)
					result.Halted = true
				}
				if !result.Halted {
					// Bounded parallel dispatch lives in scheduler.go
					// (executeToolCallsParallel); tool results are appended
					// serially here, after the join.
					for _, out := range a.executeToolCallsParallel(ctx, sessionID, choice.Message.ToolCalls) {
						totalToolCallCount++
						toolResultMsg := &store.Message{
							SessionID:  sessionID,
							Role:       "tool",
							Content:    out.result.Content,
							ToolCallID: out.call.ID,
							Name:       out.call.Function.Name,
						}
						_, _, err = a.store.AppendMessage(ctx, toolResultMsg)
						if err != nil {
							result.Error = fmt.Errorf("append tool result: %w", err)
							result.Halted = true
							break
						}
						result.Messages = append(result.Messages, *toolResultMsg)
					}
				}
			} else {
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

					// Execute tool (toolsReg.Execute handles perms check + execution + fencing + redaction).
					// RF-1.3: carry parent session ID for spawn_subagent so the tool can branch correctly without model-supplied IDs.
					toolCtx := tools.WithSessionID(ctx, sessionID)
					toolResult, err := a.toolsReg.Execute(toolCtx, tc.Function.Name, args)
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
			SessionID:  sessionID,
			Role:       "assistant",
			Content:    choice.Message.Content,
			Usage:      resp.Usage,
			Model:      model,
			DurationMs: llmElapsed,
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
		result.Metrics.TTFTMs = firstTTFTMs
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
	result.Metrics.TTFTMs = firstTTFTMs
	result.Metrics.HarnessOverheadMs = result.Metrics.DurationMs() - totalLLMTimeMs
	result.Metrics.TotalTokens = totalTokens
	result.Metrics.PromptTokens = totalPromptTokens
	result.Metrics.CompletionTokens = totalCompletionTokens
	result.Metrics.ToolCallCount = totalToolCallCount
	result.Metrics.IterationCount = iterationCount

	return result, result.Error
}

// callLLMStreamWithFailover wraps callLLMStream with the same fallback_chain
// walk as Registry.ChatWithFallback, restricted to failures that happen
// before any token reaches the caller (ttftMs == 0): once onDelta has fired
// once, the caller has already been shown partial output, and silently
// restarting on a different model would splice two half-answers together —
// so a mid-stream failure (ttftMs > 0) always stops here, exactly like plain
// callLLMStream. A pre-first-token failure is indistinguishable in effect
// from a non-streaming failure (nothing shown yet), so it's retried the same
// way: only on IsRetryable, one chain entry at a time, stopping immediately
// on a non-retryable error or once the chain is exhausted. Requires the
// registry to implement fallbackChainSource; without it this degrades to a
// single callLLMStream attempt (mirrors chatFailover's degrade behavior).
func (a *Agent) callLLMStreamWithFailover(ctx context.Context, defaultProvider llm.Provider, defaultModel string, req llm.ChatRequest, onDelta func(string), startTime time.Time) (llm.ChatResponse, int64, error) {
	attempt := func(provider llm.Provider, model string) (llm.ChatResponse, int64, error) {
		attemptReq := req
		attemptReq.Model = model
		resp, ttftMs, err := a.callLLMStream(ctx, provider, attemptReq, onDelta, startTime)
		if err != nil && errors.Is(err, llm.ErrStreamingNotSupported) {
			// Provider does not support streaming: exact Chat fallback (WU3),
			// same as the non-failover streaming path.
			resp, err = provider.Chat(ctx, attemptReq)
			ttftMs = 0
		}
		return resp, ttftMs, err
	}

	resp, ttftMs, err := attempt(defaultProvider, defaultModel)
	if err == nil || ttftMs > 0 || !llm.IsRetryable(err) {
		return resp, ttftMs, err
	}
	chainSrc, ok := a.llmReg.(fallbackChainSource)
	if !ok {
		return resp, ttftMs, err
	}

	errs := []error{fmt.Errorf("default/%s: %w", defaultModel, err)}
	for _, target := range chainSrc.FallbackChain() {
		fbResp, fbTTFT, fbErr := attempt(target.Provider, target.Model)
		if fbErr == nil {
			a.logger.Warn("fell back to next model in fallback_chain (streaming)",
				"from_model", defaultModel,
				"to_provider", target.ProviderName, "to_model", target.Model)
			return fbResp, fbTTFT, nil
		}
		errs = append(errs, fmt.Errorf("%s/%s: %w", target.ProviderName, target.Model, fbErr))
		if fbTTFT > 0 || !llm.IsRetryable(fbErr) {
			return llm.ChatResponse{}, fbTTFT, fmt.Errorf("fallback_chain exhausted: %w", errors.Join(errs...))
		}
	}
	return llm.ChatResponse{}, 0, fmt.Errorf("fallback_chain exhausted: %w", errors.Join(errs...))
}

// callLLMStream attempts streaming and assembles a ChatResponse.
// On any mid-stream failure it returns an error and the caller must fail the turn
// (documented contract: no fallback within the same turn; the next turn may be non-streaming).
// Only ErrStreamingNotSupported before first token may fallback to Chat — or, when called
// through callLLMStreamWithFailover, a pre-first-token retryable failure may fall
// to the next fallback_chain entry instead.
// It returns TTFT (time-to-first-token) in milliseconds, 0 if no token was emitted.
func (a *Agent) callLLMStream(ctx context.Context, provider llm.Provider, req llm.ChatRequest, onDelta func(string), startTime time.Time) (llm.ChatResponse, int64, error) {
	streamer, ok := provider.(ChatStreamer)
	if !ok {
		return llm.ChatResponse{}, 0, fmt.Errorf("%w: provider does not implement ChatStream", llm.ErrStreamingNotSupported)
	}
	// Ensure request signals streaming for providers that inspect it.
	req.Stream = true
	ch, err := streamer.ChatStream(ctx, req)
	if err != nil {
		return llm.ChatResponse{}, 0, err
	}
	if ch == nil {
		return llm.ChatResponse{}, 0, errors.New("nil stream channel")
	}
	content, toolCalls, usage, finishReason, ttftMs, cErr := consumeStream(ctx, ch, onDelta, startTime)
	if cErr != nil {
		return llm.ChatResponse{}, ttftMs, cErr
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
	}, ttftMs, nil
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
// A chunk with Error != "" is treated as terminal mid-stream failure (fails turn).
// TTFT is measured as time from startTime to first non-empty text delta; 0 if none.
// Context cancellation is respected and produces a context error.
func consumeStream(ctx context.Context, ch <-chan llm.StreamChunk, onDelta func(string), startTime time.Time) (string, []llm.ToolCall, *llm.Usage, string, int64, error) {
	var (
		contentBuilder strings.Builder
		toolCalls      []llm.ToolCall
		usage          *llm.Usage
		finishReason   string
		ttftMs         int64
		ttftSet        bool
	)
	for {
		select {
		case <-ctx.Done():
			return "", nil, nil, "", ttftMs, ctx.Err()
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
				return contentBuilder.String(), toolCalls, usage, finishReason, ttftMs, nil
			}
			if chunk.Error != "" {
				return "", nil, nil, "", ttftMs, fmt.Errorf("stream error: %s", chunk.Error)
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					if !ttftSet {
						elapsed := time.Since(startTime)
						ttftMs = elapsed.Milliseconds()
						if ttftMs == 0 {
							ttftMs = 1
						}
						ttftSet = true
					}
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
