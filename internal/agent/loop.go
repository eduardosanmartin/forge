// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
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

// Agent orchestrates the agent loop: user message -> assistant -> tool calls -> ... -> final answer.
type Agent struct {
	ctxAssembler  *ContextAssembler
	llmReg        LLMRegistryInterface
	toolsReg      ToolsRegistryInterface
	permsEngine   PermsEngineInterface
	store         StoreInterface
	logger        *slog.Logger
	maxIterations int
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
	maxIterations := 10 // default
	// Could be made configurable via config in the future
	return &Agent{
		ctxAssembler:  NewContextAssembler(toolsReg, store, 10), // default 10 turns history
		llmReg:        llmReg,
		toolsReg:      toolsReg,
		permsEngine:   permsEngine,
		store:         store,
		logger:        logger,
		maxIterations: maxIterations,
	}
}

// ExecuteTurn runs one complete turn: user message -> assistant -> tool calls -> ... -> final answer.
func (a *Agent) ExecuteTurn(ctx context.Context, sessionID string, userMessage string) (TurnResult, error) {
	startTime := time.Now()
	result := TurnResult{
		Metrics: TurnMetrics{
			StartTime: startTime,
		},
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
			result.Error = fmt.Errorf("max iterations (%d) exceeded", a.maxIterations)
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

		req := llm.ChatRequest{
			Model:    model,
			Messages: llmMessages,
			Tools:    toolDefs,
			Stream:   false,
		}

		resp, err := provider.Chat(ctx, req)
		llmElapsed := time.Since(llmStartTime).Milliseconds()
		totalLLMTimeMs += llmElapsed

		if err != nil {
			result.Error = fmt.Errorf("llm chat: %w", err)
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
