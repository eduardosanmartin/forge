// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
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
Call ONLY tools from the provided tool list — never invent tool names. If you
need a capability you do not see, say so in text instead of guessing a name.

To list files, call fs_list with {"path": "."} for the workspace root. The
path argument is always required for fs_* tools — never call them without it.

If a tool call returns an ERROR, read the message, fix exactly what it names,
and retry the corrected call. Do not abandon the task after one tool error.

Optional arguments with a default: OMIT them entirely instead of inventing
values (never guess directories such as /workspace or /tmp). If a call fails
because of an invented value, retry the identical call without that argument.

Not every request needs a tool call. If the user's message doesn't reference
this project, its files, or its state, answer directly from your own
knowledge — do not explore the filesystem "just in case." Use tools when the
request actually requires reading, writing, or inspecting this project (or
anything else a tool exists for).`

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
	// Skills is the skills manager for lazy-load semantic injection (RF-4.2).
	// Nil disables skills injection.
	Skills *skill.Manager
	// SkillsLazyLoad selects semantic matching (true) vs. manual activation
	// (false) — see config.SkillsConfig and Build's Skills branch.
	SkillsLazyLoad bool
	// SkillsEnabled is the project's skills.enabled config list, used only
	// when SkillsLazyLoad is false.
	SkillsEnabled []string
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
// Returns []llm.Message ready for ChatRequest. Equivalent to
// BuildWithQuery(ctx, sessionID, userMessage, userMessage).
func (c *ContextAssembler) Build(ctx context.Context, sessionID string, userMessage string) ([]llm.Message, error) {
	return c.BuildWithQuery(ctx, sessionID, userMessage, userMessage)
}

// BuildWithQuery is Build with the retrieval/skills query separated from
// the message to append. The agent loop passes the turn's ORIGINAL user
// message as query on every iteration of the turn, and userMessage = ""
// on tool-result continuations: previously the query was tied to
// userMessage, so retrieval and skill instructions silently vanished from
// the second iteration on — the model lost its skills mid-task, and the
// prefix changed between iterations of the same turn (defeating KV-cache
// reuse).
func (c *ContextAssembler) BuildWithQuery(ctx context.Context, sessionID string, userMessage, query string) ([]llm.Message, error) {
	var messages []llm.Message

	// 1. System prompt (fixed per session)
	messages = append(messages, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// 2. Tool definitions travel ONLY via ChatRequest.Tools (see ToolDefs
	// below), which OpenAI-compatible providers consume natively as
	// structured function schemas. This used to ALSO inject one
	// "TOOL: name - description" system message per tool here, restating
	// the same name+description the structured schema already carries —
	// pure duplication (confirmed: ~1.5-2K wasted prompt tokens per turn
	// with the default 16-tool registry), and especially costly for small
	// local models already fighting a tight context window.

	// 3. Session-scoped context: v0 anchored facts plus the v1 feature
	// injections (anchoring, retrieval, compaction), all gated by the
	// session's flag metadata. The v1 routing flag needs no context
	// injection: it changes which model the agent loop selects, not what
	// the model sees.
	enableRetrieval := false
	enableCompaction := false
	enableAnchoring := false
	enableSkills := false
	session, err := c.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			// Session not found is not fatal for context building; proceed
			// without session-scoped injections (anchored facts, v1 flags).
		} else {
			return nil, fmt.Errorf("get session: %w", err)
		}
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
			if v, ok := session.Metadata["v1_skills"].(bool); ok {
				enableSkills = v
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

	}

	// 4. History window, computed BEFORE the retrieval/skills injections so
	// retrieval can skip messages the model already sees verbatim. With
	// compaction enabled and the compactor wired, sessions longer than
	// compactionThreshold persisted messages get a compacted VIEW: one
	// deterministic summary system message for the older turns plus the
	// most recent turns verbatim. It is computed statelessly from the
	// fetched transcript on every Build — no marker in session metadata —
	// because the Compactor is deterministic and cheap to re-run, and a
	// marker could go stale across flag toggles. Non-destructive by
	// construction: the SQLite store keeps the full transcript; only what
	// the model sees changes.
	var summary string
	var window []store.Message
	compacted := false
	if enableCompaction && c.v1Deps.Compactor != nil {
		summary, window, compacted = c.compactedHistory(ctx, sessionID)
	}
	if !compacted {
		recent, err := c.recentTranscript(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("get recent messages: %w", err)
		}
		window = selectHistoryWindow(recent, c.maxHistoryTurns*2)
	}

	// V1: Retrieval — inject the chunks of THIS session's indexed history
	// most similar to the turn's request, skipping any message already in
	// the verbatim window (repeating it would only spend tokens). The index
	// is in-memory per daemon process and per session; the SessionManager
	// indexes new messages incrementally after each turn. Empty index, no
	// hits, or an empty query → no injection.
	if enableRetrieval && c.v1Deps.Retriever != nil && query != "" {
		inWindow := make(map[int64]bool, len(window))
		for _, m := range window {
			if m.ID != 0 {
				inWindow[m.ID] = true
			}
		}
		if chunks, searchErr := c.v1Deps.Retriever.SearchSession(sessionID, query, retrievalTopK, inWindow); searchErr == nil && len(chunks) > 0 {
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

	// Skills (v1, RF-4.2): two mutually exclusive activation modes.
	// LazyLoad true: semantic matching — only enabled skills whose
	// description matches the turn's request get injected
	// (Skills.Relevant, scored against an embedding).
	// LazyLoad false: manual activation — no matching call at all.
	// The active set is whatever Skills.ActiveManual(SkillsEnabled)
	// resolves to (project's configured list + every global skill),
	// injected unconditionally on every turn regardless of message
	// content. See config.SkillsConfig's doc comment for why false is
	// the honest default today.
	if enableSkills && c.v1Deps.Skills != nil && query != "" {
		var skills []skill.Skill
		if c.v1Deps.SkillsLazyLoad {
			skills, _ = c.v1Deps.Skills.Relevant(query)
		} else {
			skills = c.v1Deps.Skills.ActiveManual(c.v1Deps.SkillsEnabled)
		}
		for _, sk := range skills {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: fmt.Sprintf("SKILL INSTRUCTIONS (v1) [%s]:\n%s", sk.Name, sk.Instructions),
			})
		}
	}

	if compacted {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: "COMPACTED HISTORY (v1):\n" + summary,
		})
	}
	for _, msg := range window {
		messages = append(messages, toLLMMessage(msg))
	}

	// 5. Current user message. Callers (the agent loop) persist the user
	// message to the store BEFORE calling Build, so step 4's history window
	// already ends with it — appending it again here would duplicate it in
	// every request (confirmed in production: the same user text appeared
	// twice in one LLM call). Skip the append when the last message already
	// assembled is that exact user turn; on tool-result continuation
	// iterations userMessage is "" and nothing is appended here at all (the
	// continuation's own history window, ending in tool results, is enough).
	if userMessage != "" {
		last := len(messages) - 1
		alreadyPresent := last >= 0 && messages[last].Role == "user" && messages[last].Content == userMessage
		if !alreadyPresent {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: userMessage,
			})
		}
	}

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

// compactedHistory returns the compacted view of the session history — the
// joined summaries of the older turns and the verbatim window — when the
// session exceeds compactionThreshold persisted messages. ok reports
// whether the compacted view applies; when false
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
func (c *ContextAssembler) compactedHistory(ctx context.Context, sessionID string) (summary string, window []store.Message, ok bool) {
	transcript, err := c.store.GetMessagesSince(ctx, sessionID, 0)
	if err != nil || len(transcript) <= compactionThreshold {
		return "", nil, false
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
		return "", nil, false
	}

	var summaries []string
	for _, t := range compactedTurns {
		if t.Summary != "" {
			summaries = append(summaries, t.Summary)
		}
	}
	if len(summaries) == 0 {
		return "", nil, false
	}
	return strings.Join(summaries, "\n"), selectHistoryWindow(transcript, c.maxHistoryTurns*2), true
}

// historyPageSize is how many messages recentTranscript fetches per store
// round trip, and historyScanLimit caps how far back it looks for the
// current turn's opening user message (a turn longer than this is
// pathological: max_iterations bounds it far below).
const (
	historyPageSize  = 64
	historyScanLimit = 4096
)

// recentTranscript returns, oldest first, the tail of the session's
// transcript that selectHistoryWindow needs: enough to contain the current
// turn's opening user message plus the older-history budget. Usually one
// store call; more only for turns with very many tool iterations.
func (c *ContextAssembler) recentTranscript(ctx context.Context, sessionID string) ([]store.Message, error) {
	older := c.maxHistoryTurns * 2
	var newestFirst []store.Message
	for offset := 0; offset < historyScanLimit; offset += historyPageSize {
		page, err := c.store.GetMessages(ctx, sessionID, historyPageSize, offset)
		if err != nil {
			return nil, err
		}
		newestFirst = append(newestFirst, page...)
		if userIdx := firstUserIndex(newestFirst); userIdx >= 0 && len(newestFirst) >= userIdx+1+older {
			break
		}
		if len(page) < historyPageSize {
			break // transcript exhausted
		}
	}
	out := make([]store.Message, len(newestFirst))
	for i, m := range newestFirst {
		out[len(newestFirst)-1-i] = m
	}
	return out, nil
}

func firstUserIndex(newestFirst []store.Message) int {
	for i, m := range newestFirst {
		if m.Role == "user" {
			return i
		}
	}
	return -1
}

// selectHistoryWindow picks the history the model sees from a
// chronological (oldest-first) transcript tail:
//
//   - The CURRENT turn — from the latest user message to the end — is
//     always included whole. A fixed message-count window used to cut it:
//     after ~8 tool iterations the user's own request fell out of the
//     request and the model kept working without knowing the task.
//   - Earlier history fills what is left of olderBudget (the total
//     message budget) after the current turn, trimmed to
//     start on a user message (a whole-turn boundary). That also guarantees
//     the window never opens with an orphaned tool result or an assistant
//     tool call whose results were cut. An orphaned tool result (its
//     ToolCallID matching no tool_calls entry in the request) gets the
//     whole request rejected by strict providers — observed against
//     OpenCode Zen: HTTP 400 "tool result's tool id ... not found".
//
// With no user message at all (legacy or synthetic transcripts), it falls
// back to the last budget messages minus any orphaned tool prefix.
func selectHistoryWindow(transcript []store.Message, budget int) []store.Message {
	olderBudget := budget
	lastUser := -1
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		start := len(transcript) - olderBudget
		if start < 0 {
			start = 0
		}
		tail := transcript[start:]
		i := 0
		for i < len(tail) && tail[i].Role == "tool" {
			i++
		}
		return tail[i:]
	}

	// The budget covers the whole window: the current turn spends it first
	// (it is never cut), earlier turns get what is left.
	olderBudget -= len(transcript) - lastUser
	if olderBudget < 0 {
		olderBudget = 0
	}
	olderStart := lastUser - olderBudget
	if olderStart < 0 {
		olderStart = 0
	}
	older := transcript[olderStart:lastUser]
	// Align the older part to a turn boundary.
	cut := len(older)
	for i, m := range older {
		if m.Role == "user" {
			cut = i
			break
		}
	}
	if olderStart == 0 && len(older) > 0 && older[0].Role != "user" {
		// The transcript itself starts mid-turn (no earlier user message
		// exists to align to): keep it, minus an orphaned tool prefix.
		cut = 0
		for cut < len(older) && older[cut].Role == "tool" {
			cut++
		}
	}
	window := make([]store.Message, 0, len(older)-cut+len(transcript)-lastUser)
	window = append(window, older[cut:]...)
	window = append(window, transcript[lastUser:]...)
	return window
}

// toLLMMessage converts a persisted store.Message into the llm.Message shape
// ChatRequest consumes.
func toLLMMessage(msg store.Message) llm.Message {
	return llm.Message{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCalls:  msg.ToolCalls,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	}
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
