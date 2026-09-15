package run

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decompositionSystemPrompt instructs the model to propose an atomic task
// breakdown as pure JSON. Written for a capable/reasoning-tier model doing
// the planning — the resulting tasks are meant to be small enough for a
// cheaper/local model to execute one at a time (see sugerenciasDeClaude.md
// §5: fragmenting work for small local LLMs). Deliberately verbose about the
// atomicity and done_criteria conventions: a vague or missing done_criteria
// defeats the entire point of mechanical verification (checkDoneCriteria).
const decompositionSystemPrompt = `You are a technical planner breaking a goal into an ORDERED list of small, atomic tasks for OTHER agents (possibly small, less capable ones) to execute one at a time, each with its own fresh turn.

Do NOT call any tools. Do not explore the filesystem, read files, or run commands — you have tools available in this turn, but using them here is wrong. Base the plan ENTIRELY on the GOAL and SPEC text given below; if something about the existing codebase is genuinely unclear, name that uncertainty inside a task's own goal text (e.g. "locate the existing router and register the route") rather than going to look it up yourself. Respond immediately with the JSON array — nothing else.

Rules for each task:
- Touch ONE file or ONE function/unit of work — never a whole subsystem. If a task feels bigger than that, split it into more tasks.
- Expect it to need 3 tool calls or fewer to complete. If you can't imagine finishing it in that budget, split it further.
- Give it a done_criteria that is a SINGLE PROGRAM INVOCATION whenever the outcome can be checked mechanically (build, test, lint) — prefix it with "cmd: " exactly, e.g. "cmd: go build ./internal/foo/..." or "cmd: go test ./internal/foo/...". It runs directly via exec, NOT through a shell: exactly one program plus its plain arguments, NEVER "&&", "|", ";", redirects, subshells, or env var expansion. If you need to verify two things, either pick the single most meaningful check or split into two tasks. Only fall back to descriptive text (no "cmd:" prefix) when no single command can verify it.
- Give it a short file_budget naming the file(s)/path(s) it's expected to touch, when known.
- Give it a model_hint of "cheap", "generation", or "reasoning" — your best guess at how much capability executing it actually needs (most atomic tasks should be "cheap" or "generation"; reserve "reasoning" for genuinely hard ones).
- IDs are short kebab-case strings, unique within the list, ordered so earlier tasks unblock later ones.

Respond with ONLY a JSON array, no prose before or after, no markdown code fences. Each element:
{"id": "...", "goal": "...", "done_criteria": "...", "file_budget": "...", "model_hint": "..."}`

// BuildDecompositionPrompt renders the full user-turn text for a
// decomposition call: the planning instructions (decompositionSystemPrompt)
// followed by the goal and, when present, the spec text. Folded into one
// message because the decomposition call goes through the same
// session.execute_turn RPC as any other turn (no separate system-prompt
// channel exists there) — see client.ManifestDecomposer.
func BuildDecompositionPrompt(goal, spec string) string {
	var sb strings.Builder
	sb.WriteString(decompositionSystemPrompt)
	sb.WriteString("\n\nGOAL:\n")
	sb.WriteString(strings.TrimSpace(goal))
	if s := strings.TrimSpace(spec); s != "" {
		sb.WriteString("\n\nSPEC:\n")
		sb.WriteString(s)
	}
	return sb.String()
}

// decomposedTask mirrors Task's JSON shape for parsing model output
// independently of Task's own struct tags (keeps the wire contract explicit
// and decoupled from any future internal renames of Task).
type decomposedTask struct {
	ID           string `json:"id"`
	Goal         string `json:"goal"`
	DoneCriteria string `json:"done_criteria"`
	FileBudget   string `json:"file_budget"`
	ModelHint    string `json:"model_hint"`
}

// ParseDecomposedTasks extracts a []Task from a model's raw text response to
// the decomposition prompt. Models routinely ignore "no prose/no fences"
// instructions, so this tries progressively looser extraction rather than
// failing on the first non-strict-JSON response:
//  1. the raw text as-is;
//  2. with a leading/trailing ```json or ``` fence stripped;
//  3. the substring from the first '[' to the last ']' in the text.
//
// Returns an error naming what was tried when none of them parse — callers
// (the manifest's after_spec_decomposition checkpoint) surface that to a
// human rather than silently falling back to a single giant task.
func ParseDecomposedTasks(raw string) ([]Task, error) {
	candidates := []string{raw, stripCodeFence(raw)}
	if start, end, ok := outermostBrackets(raw); ok {
		candidates = append(candidates, raw[start:end])
	}

	var lastErr error
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		var decoded []decomposedTask
		if err := json.Unmarshal([]byte(c), &decoded); err != nil {
			lastErr = err
			continue
		}
		tasks := make([]Task, 0, len(decoded))
		for _, d := range decoded {
			tasks = append(tasks, Task{
				ID:           d.ID,
				Goal:         d.Goal,
				DoneCriteria: d.DoneCriteria,
				FileBudget:   d.FileBudget,
				ModelHint:    d.ModelHint,
			})
		}
		if len(tasks) == 0 {
			lastErr = fmt.Errorf("decoded an empty task list")
			continue
		}
		return tasks, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no JSON array found in response")
	}
	return nil, fmt.Errorf("could not parse a task list from the decomposition response: %w", lastErr)
}

// stripCodeFence removes a single leading/trailing markdown code fence
// (``` or ```json) if present; returns s unchanged otherwise.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// outermostBrackets returns the span [start, end) of the JSON array starting
// at the first '[' in s, found by tracking bracket depth (not just "first
// '[' to last ']'" — a naive last-']' scan grabs trailing prose as part of
// the array whenever the model's own commentary after the array contains
// ANY ']' of its own, e.g. a markdown link or an "arr[0]" example; observed
// in practice against a real decomposition response and exactly the kind of
// response shape small/local models are prone to). Bracket/brace depth
// inside JSON string literals is ignored via a small state machine so a
// string value like "returns arr[0]" doesn't miscount.
func outermostBrackets(s string) (start, end int, ok bool) {
	start = strings.IndexByte(s, '[')
	if start < 0 {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return 0, 0, false
}
