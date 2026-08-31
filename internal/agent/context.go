// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
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
// Order: system prompt + tool definitions + anchored memory + retrieval + compaction + recent history + current user message.
type ContextAssembler struct {
	toolsReg        ToolsRegistryInterface
	store           StoreInterface
	maxHistoryTurns int
	v1Deps          V1Deps
}

// V1Deps groups the optional dependencies behind the v1 feature flags.
// Every field is nil-safe: a nil dependency simply disables the
// corresponding context injection in Build, keeping flag semantics intact
// for binaries and tests constructed without the full wiring.
type V1Deps struct {
	Retriever   *retrieval.Retriever
	Compactor   *compaction.Compactor
	AnchorStore *anchor.AnchorStoreSQL
}

// SetV1Deps wires the optional v1 feature dependencies. Intended to be
// called once at construction time, before any Build call.
func (c *ContextAssembler) SetV1Deps(deps V1Deps) {
	c.v1Deps = deps
}

// NewContextAssembler creates a new ContextAssembler.
// maxHistoryTurns defaults to 8 if <= 0 (RNF-10-tuned, see the bench).
func NewContextAssembler(toolsReg ToolsRegistryInterface, store StoreInterface, maxHistoryTurns int) *ContextAssembler {
	if maxHistoryTurns <= 0 {
		maxHistoryTurns = 8
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

	// 2. Tool definitions (fixed order: fs_read, fs_write, fs_list, shell_exec, git)
	toolDefs := c.toolsReg.List()
	for _, t := range toolDefs {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("TOOL: %s - %s", t.Name(), t.Description()),
		})
	}

	// 3. Session-scoped context: v0 anchored facts plus the v1 feature
	// injections (anchoring, retrieval, compaction), all gated by the
	// session's flag metadata. The v1 routing flag needs no context
	// injection: it changes which model the agent loop selects, not what
	// the model sees.
	enableRetrieval := false
	enableCompaction := false
	enableAnchoring := false
	session, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		// Session not found is not fatal for context building; we'll proceed without anchored facts
		// but we should still return an error if it's something other than not found
		// For now, we'll just skip anchored memory if session not found
		// TODO: handle ErrSessionNotFound specifically when available
	} else {
		// v0 anchored facts
		if anchoredFacts, ok := session.Metadata["anchored_facts"].(string); ok && anchoredFacts != "" {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "ANCHORED FACTS: " + anchoredFacts,
			})
		}

		// V1: Check for v1 features enabled in session metadata
		if session.Metadata != nil {
			if v, ok := session.Metadata["v1_retrieval"].(bool); ok {
				enableRetrieval = v
			}
			if v, ok := session.Metadata["v1_compaction"].(bool); ok {
				enableCompaction = v
			}
			if v, ok := session.Metadata["v1_anchoring"].(bool); ok {
				enableAnchoring = v
			}
		}

		// V1: Anchored memory. The assembler queries the anchor store
		// directly — nil-safe: the injection happens only when the
		// dependency is wired AND the session flag is on. Nothing writes
		// session.Metadata["anchors"] anymore; v0 anchored_facts (above)
		// is untouched. A store error skips the injection rather than
		// failing the turn.
		if enableAnchoring && c.v1Deps.AnchorStore != nil {
			if anchors, listErr := c.v1Deps.AnchorStore.List(ctx, sessionID); listErr == nil && len(anchors) > 0 {
				var sb strings.Builder
				sb.WriteString("ANCHORED FACTS (v1):\n")
				for _, a := range anchors {
					if a.Content != "" {
						sb.WriteString(fmt.Sprintf("- %s\n", a.Content))
					}
				}
				messages = append(messages, llm.Message{
					Role:    "system",
					Content: sb.String(),
				})
			}
		}

		// V1: Retrieval — inject the chunks of indexed history most
		// similar to the current user message. The index is in-memory per
		// daemon process (acceptable for v1): the SessionManager re-indexes
		// the session transcript after each turn. Empty index, no hits, or
		// an empty user message → no injection.
		if enableRetrieval && c.v1Deps.Retriever != nil && userMessage != "" {
			if chunks, searchErr := c.v1Deps.Retriever.Search(userMessage, retrievalTopK); searchErr == nil && len(chunks) > 0 {
				var sb strings.Builder
				sb.WriteString("RELEVANT CONTEXT (v1):\n")
				for _, ch := range chunks {
					sb.WriteString(fmt.Sprintf("- [%s] %s (score %.2f)\n", ch.Role, ch.Content, ch.Score))
				}
				messages = append(messages, llm.Message{
					Role:    "system",
					Content: sb.String(),
				})
			}
		}
	}

	// 4. History window. With compaction enabled and the compactor wired,
	// sessions longer than compactionThreshold persisted messages get a
	// compacted VIEW: one deterministic summary system message for the
	// older turns plus the most recent turns verbatim. It is computed
	// statelessly from the fetched transcript on every Build — no marker
	// in session metadata — because the Compactor is deterministic and
	// cheap to re-run, and a marker could go stale across flag toggles.
	// Non-destructive by construction: the SQLite store keeps the full
	// transcript; only what the model sees changes.
	compacted := false
	if enableCompaction && c.v1Deps.Compactor != nil {
		compacted = c.appendCompactedHistory(ctx, sessionID, &messages)
	}

	if !compacted {
		// Recent history (sliding window of last N messages, configurable)
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
	}

	// 5. Current user message
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: userMessage,
	})

	return messages, nil
}

// retrievalTopK is how many similar history chunks the v1 retrieval
// injection adds ahead of the current user message.
const retrievalTopK = 3

// compactionThreshold is the persisted message count above which the v1
// compaction flag switches the model's view to summary + recent turns.
// compaction.Config defines no count threshold (its fields are model names
// and an anchor score), so the threshold lives here as a named constant.
const compactionThreshold = 40

// appendCompactedHistory appends the compacted view of the session history
// to messages when the session exceeds compactionThreshold persisted
// messages. It reports whether the compacted view was applied; when false
// (session at or below the threshold, or any store/compactor error) the
// caller falls back to the plain sliding window.
//
// The older turns are summarized by Compactor.Compact (deterministic, no
// LLM call); the most recent turns stay verbatim using the same sliding
// window Build always applies, taken from the original transcript rather
// than the Compactor's output so tool_call fields survive intact. The full
// transcript is fed to Compact so every message outside the verbatim
// window is covered by a summary (Compact's internal keep-recent slice
// overlaps the window instead of leaving a gap).
func (c *ContextAssembler) appendCompactedHistory(ctx context.Context, sessionID string, messages *[]llm.Message) bool {
	transcript, err := c.store.GetMessagesSince(ctx, sessionID, 0)
	if err != nil || len(transcript) <= compactionThreshold {
		return false
	}

	turns := make([]compaction.Turn, 0, len(transcript))
	for _, msg := range transcript {
		turns = append(turns, compaction.Turn{
			Role:    msg.Role,
			Content: msg.Content,
			Tokens:  len(msg.Content) / 4, // same rough estimate Compact uses
		})
	}
	compactedTurns, _, err := c.v1Deps.Compactor.Compact(turns)
	if err != nil {
		return false
	}

	var summaries []string
	for _, t := range compactedTurns {
		if t.Summary != "" {
			summaries = append(summaries, t.Summary)
		}
	}
	if len(summaries) == 0 {
		return false
	}

	*messages = append(*messages, llm.Message{
		Role:    "system",
		Content: "COMPACTED HISTORY (v1):\n" + strings.Join(summaries, "\n"),
	})

	window := c.maxHistoryTurns * 2
	if window > len(transcript) {
		window = len(transcript)
	}
	for _, msg := range transcript[len(transcript)-window:] {
		*messages = append(*messages, llm.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}
	return true
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
