// Package agent implements the forge agent loop with stable context prefix
// layout, tool-calling orchestration, and base metrics.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
// Order (see BuildWithQuery): system prompt + tool definitions (via
// ChatRequest.Tools) + anchored memory + manual skills + compacted blocks +
// earlier history + per-turn block (retrieval, matched skills) + current turn.
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
	// RepoMap, when set, renders the workspace's repo map (F5) for the
	// stable part of the prompt. Nil disables it.
	RepoMap func() string
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
	var summaries []string
	var window []store.Message
	compacted := false
	if enableCompaction && c.v1Deps.Compactor != nil {
		summaries, window, compacted = c.compactedHistory(ctx, sessionID)
	}
	if !compacted {
		recent, err := c.recentTranscript(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("get recent messages: %w", err)
		}
		window = selectHistoryWindow(recent, c.maxHistoryTurns*2)
	}

	// Ordering for prompt/KV-cache reuse (RNF-2.2/2.4): everything that
	// stays put across turns first, then the history that only grows, and
	// the per-turn material LAST, right before the current turn:
	//
	//   system → anchors → manual skills → [compacted summary]
	//          → earlier turns (append-only between window steps)
	//          → retrieval + relevance-matched skills (vary per turn)
	//          → current turn (its request + this turn's tool iterations)
	//
	// Retrieval and lazily matched skills used to sit right after the
	// system prompt; since they change with every request, they
	// invalidated the cached prefix for the entire history behind them on
	// every turn. In this order a new turn re-processes only the per-turn
	// block plus what was appended since the previous turn, and the
	// iterations of one turn share everything but their newest messages.
	split := len(window)
	for i := len(window) - 1; i >= 0; i-- {
		if window[i].Role == "user" {
			split = i
			break
		}
	}
	earlier, current := window[:split], window[split:]

	var volatile []llm.Message

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
				sb.WriteString(fmt.Sprintf("- [%s] %s (score %.2f)\n", ch.Role, truncateRunes(ch.Content, retrievalSnippetChars), ch.Score))
			}
			volatile = append(volatile, llm.Message{
				Role:    "system",
				Content: sb.String(),
			})
		}
	}

	// Skills (v1, RF-4.2): two mutually exclusive activation modes.
	// LazyLoad true: semantic matching — only enabled skills whose
	// description matches the turn's request get injected
	// (Skills.Relevant, scored against an embedding); they vary per turn,
	// so they go in the volatile block.
	// LazyLoad false: manual activation — no matching call at all.
	// The active set is whatever Skills.ActiveManual(SkillsEnabled)
	// resolves to (project's configured list + every global skill),
	// injected unconditionally on every turn regardless of message
	// content — stable, so part of the cached prefix. See
	// config.SkillsConfig's doc comment for why false is the honest
	// default today.
	if enableSkills && c.v1Deps.Skills != nil && query != "" {
		if c.v1Deps.SkillsLazyLoad {
			skills, _ := c.v1Deps.Skills.Relevant(query)
			for _, sk := range skills {
				volatile = append(volatile, skillMessage(sk))
			}
		} else {
			for _, sk := range c.v1Deps.Skills.ActiveManual(c.v1Deps.SkillsEnabled) {
				messages = append(messages, skillMessage(sk))
			}
		}
	}

	// Repo map (F5): stable across turns unless declarations change, so it
	// belongs in the cached prefix, before any history.
	if c.v1Deps.RepoMap != nil {
		if m := c.v1Deps.RepoMap(); m != "" {
			messages = append(messages, llm.Message{Role: "system", Content: m})
		}
	}

	// One system message per summarized block: a new block appends a
	// message instead of rewriting a shared one, so earlier blocks keep
	// matching the cached prefix.
	for i, sum := range summaries {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("COMPACTED HISTORY (v1): [part %d]\n%s", i+1, sum),
		})
	}
	for _, msg := range earlier {
		messages = append(messages, toLLMMessage(msg))
	}
	messages = append(messages, volatile...)
	for _, msg := range current {
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

// skillMessage renders one skill's instructions as a system message.
func skillMessage(sk skill.Skill) llm.Message {
	return llm.Message{
		Role:    "system",
		Content: fmt.Sprintf("SKILL INSTRUCTIONS (v1) [%s]:\n%s", sk.Name, sk.Instructions),
	}
}

// retrievalTopK is how many similar history chunks the v1 retrieval
// injection adds ahead of the current user message.
const retrievalTopK = 3

// retrievalSnippetChars caps each retrieved chunk. The retrieval block is
// re-processed on every turn (it changes with each request), so every
// character in it costs prefill time on each turn; a pointer-sized snippet
// is enough to remind the model of an earlier exchange.
const retrievalSnippetChars = 300

// truncateRunes cuts s to at most max bytes on a rune boundary, marking
// the cut with "…".
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// compactionThreshold is the persisted message count above which the v1
// compaction flag switches the model's view to summary + recent turns.
// compaction.Config defines no count threshold (its fields are model names
// and an anchor score), so the threshold lives here as a named constant.
const compactionThreshold = 40

// compactedHistory returns the compacted view of the session history —
// one summary per completed block of older turns, and the verbatim window
// — when the session exceeds compactionThreshold persisted messages. ok
// reports whether the compacted view applies; when false (session at or
// below the threshold, or a store error) the caller falls back to the
// plain window.
//
// Block-stable by construction (RNF-2.2/2.4): block boundaries sit at the
// same turn-aligned, windowStep-quantized positions the verbatim window
// starts at, so the window always begins exactly where the last summarized
// block ends (no gap, no overlap), and a block's summary is a pure function
// of that block's messages — once written it never changes. Previously the
// whole summary was recomputed over a sliding split, so it changed on
// every turn and invalidated the cached prefix of everything after it
// (measured offline: ~4k re-processed tokens per turn vs ~250 for plain
// full history). Non-destructive: the store keeps the full transcript.
func (c *ContextAssembler) compactedHistory(ctx context.Context, sessionID string) (summaries []string, window []store.Message, ok bool) {
	transcript, err := c.store.GetMessagesSince(ctx, sessionID, 0)
	if err != nil || len(transcript) <= compactionThreshold {
		return nil, nil, false
	}
	budget := c.maxHistoryTurns * 2
	ws := historyWindowStart(transcript, budget)
	if ws == 0 {
		return nil, nil, false
	}
	step := windowStep(budget)
	blockStart := 0
	for blockStart < ws {
		next := alignForward(transcript, blockStart+1)
		// The next boundary: the first turn start at or after the next
		// step multiple — the same rule historyWindowStart applies.
		target := ((absPos(transcript, blockStart) / step) + 1) * step
		for next < ws && absPos(transcript, next) < target {
			next = alignForward(transcript, next+1)
		}
		if next > ws {
			next = ws
		}
		summaries = append(summaries, c.v1Deps.Compactor.SummarizeBlock(toCompactionTurns(transcript[blockStart:next])))
		blockStart = next
	}
	return summaries, transcript[ws:], true
}

func toCompactionTurns(msgs []store.Message) []compaction.Turn {
	turns := make([]compaction.Turn, 0, len(msgs))
	for _, msg := range msgs {
		turns = append(turns, compaction.Turn{
			Role:    msg.Role,
			Content: msg.Content,
			Tokens:  len(msg.Content) / 4,
		})
	}
	return turns
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
	older := c.maxHistoryTurns*2 + windowStep(c.maxHistoryTurns*2)
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
//   - Earlier history gets up to budget messages (plus up to budget/2
//     more: its start moves in steps, see historyWindowStart, so the
//     prefix stays cacheable), trimmed to start on a user message (a
//     whole-turn boundary). That also guarantees the window never opens
//     with an orphaned tool result or an assistant tool call whose results
//     were cut. An orphaned tool result (its ToolCallID matching no
//     tool_calls entry in the request) gets the whole request rejected by
//     strict providers — observed against OpenCode Zen: HTTP 400 "tool
//     result's tool id ... not found".
//
// With no user message at all (legacy or synthetic transcripts), it falls
// back to the last budget messages minus any orphaned tool prefix.
func selectHistoryWindow(transcript []store.Message, budget int) []store.Message {
	return transcript[historyWindowStart(transcript, budget):]
}

// windowStep is how far the earlier-history window start moves at once.
func windowStep(budget int) int {
	if step := budget / 2; step > 1 {
		return step
	}
	return 1
}

// absPos is a message's absolute position in its session: its Seq when
// the transcript carries usable sequence numbers (tails fetched from the
// store: positive and increasing), else its index (synthetic transcripts
// that start at the beginning).
func absPos(transcript []store.Message, i int) int {
	if seqUsable(transcript) {
		return transcript[i].Seq
	}
	return i
}

func seqUsable(transcript []store.Message) bool {
	n := len(transcript)
	return n > 0 && transcript[0].Seq > 0 && transcript[n-1].Seq-transcript[0].Seq == n-1
}

// alignForward returns the first index >= from holding a user message
// (a turn boundary), or len(transcript) when there is none.
func alignForward(transcript []store.Message, from int) int {
	for i := from; i < len(transcript); i++ {
		if transcript[i].Role == "user" {
			return i
		}
	}
	return len(transcript)
}

// historyWindowStart returns the index in transcript where the model's
// verbatim history window begins (see selectHistoryWindow).
//
// The start is quantized on ABSOLUTE positions (message Seq): the target
// position lastUser-budget is rounded down to a multiple of windowStep, so
// between steps the earlier history only GROWS at its end and the prompt
// prefix — and the inference server's KV/prompt cache — stays valid from
// one turn to the next (RNF-2.2/2.4). Using absolute positions keeps the
// steps identical no matter how long a tail of the transcript was fetched.
// The earlier-history budget does NOT shrink as the current turn grows:
// that would drop the oldest messages on every tool iteration and change
// the prefix mid-turn; the current turn's growth is bounded by
// agent.max_iterations and the tool-output caps instead.
func historyWindowStart(transcript []store.Message, budget int) int {
	lastUser := -1
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		start := len(transcript) - budget
		if start < 0 {
			start = 0
		}
		for start < len(transcript) && transcript[start].Role == "tool" {
			start++
		}
		return start
	}

	step := windowStep(budget)
	target := absPos(transcript, lastUser) - budget
	if target <= absPos(transcript, 0) {
		// Everything before the current turn fits: keep the whole tail,
		// minus an orphaned tool prefix if it starts mid-turn.
		i := 0
		for i < lastUser && transcript[i].Role == "tool" {
			i++
		}
		return i
	}
	target = (target / step) * step
	from := 0
	for from < lastUser && absPos(transcript, from) < target {
		from++
	}
	return alignForward(transcript, from)
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
