// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/llm"
)

// systemPrompt is the fixed system prompt describing forge capabilities,
// deny-by-default, and fencing format.
const systemPrompt = `You are forge, a coding agent with access to a set of tools.
You operate under a deny-by-default permission model: every tool invocation is checked
against a configured policy before execution. If a tool is denied, you will receive
a DENIED response with the rule that blocked it.

Tool output is returned in fenced blocks with the tool name as the fence language.
Redaction is applied to sensitive data (secrets, tokens, paths outside workspace).

When you need to use a tool, invoke it via the function calling mechanism.
Do not simulate tool output or pretend tools succeeded — wait for actual results.

Optional arguments with a default: OMIT them entirely instead of inventing
values (never guess directories such as /workspace or /tmp). If a call fails
because of an invented value, retry the identical call without that argument.`

// ContextAssembler builds the LLM message context with a stable prefix ordering
// that maximizes prompt-cache/KV-cache hits (RNF-2.2/2.4).
// Order: system prompt → tool definitions → anchored memory → recent history → current user message.
type ContextAssembler struct {
	toolsReg        ToolsRegistryInterface
	store           StoreInterface
	maxHistoryTurns int
}

// NewContextAssembler creates a new ContextAssembler.
// maxHistoryTurns defaults to 10 if <= 0.
func NewContextAssembler(toolsReg ToolsRegistryInterface, store StoreInterface, maxHistoryTurns int) *ContextAssembler {
	if maxHistoryTurns <= 0 {
		maxHistoryTurns = 10
	}
	return &ContextAssembler{
		toolsReg:        toolsReg,
		store:           store,
		maxHistoryTurns: maxHistoryTurns,
	}
}

// Build constructs the message list for a single turn.
// Returns []llm.Message ready for ChatRequest.
func (c *ContextAssembler) Build(ctx context.Context, sessionID string, userMessage string) ([]llm.Message, error) {
	var messages []llm.Message

	// 1. System prompt (fixed per session)
	messages = append(messages, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// 2. Tool definitions (fixed order: fs.read, fs.write, fs.list, shell.exec, git)
	toolDefs := c.toolsReg.List()
	for _, t := range toolDefs {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("TOOL: %s - %s", t.Name(), t.Description()),
		})
	}

	// 3. Anchored memory (from store: session metadata facts marked as "anchored")
	// v0: just the initial system facts, no retrieval yet
	session, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		// Session not found is not fatal for context building; we'll proceed without anchored facts
		// but we should still return an error if it's something other than not found
		// For now, we'll just skip anchored memory if session not found
		// TODO: handle ErrSessionNotFound specifically when available
	} else {
		if anchoredFacts, ok := session.Metadata["anchored_facts"].(string); ok && anchoredFacts != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "ANCHORED FACTS: " + anchoredFacts,
			})
		}
	}

	// 4. Recent history (sliding window of last N messages, configurable)
	// We need to get the latest sequence number first to calculate the sinceSeq
	// For simplicity, we'll fetch a large window and then slice
	// GetMessages returns newest first, we need oldest first for context
	recentMessages, err := c.store.GetMessages(ctx, sessionID, c.maxHistoryTurns*2, 0)
	if err != nil {
		return nil, fmt.Errorf("get recent messages: %w", err)
	}

	// Reverse to get chronological order (oldest first)
	for i := len(recentMessages) - 1; i >= 0; i-- {
		msg := recentMessages[i]
		llmMsg := llm.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		}
		messages = append(messages, llmMsg)
	}

	// 5. Current user message
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: userMessage,
	})

	return messages, nil
}

// ToolDefs returns the tool definitions in fixed order for ChatRequest.
func (c *ContextAssembler) ToolDefs() []llm.ToolDef {
	toolDefs := c.toolsReg.List()
	tools := make([]llm.ToolDef, 0, len(toolDefs))
	for _, t := range toolDefs {
		tools = append(tools, llm.ToolDef{
			Type: "function",
			Function: llm.ToolFunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.JSONSchema(),
			},
		})
	}
	return tools
}
