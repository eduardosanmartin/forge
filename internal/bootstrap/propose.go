package bootstrap

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/llmjson"
)

// proposeSystemPrompt instructs the model to propose an initial RF/RNF set
// as pure JSON, mirroring internal/run/decompose.go's
// decompositionSystemPrompt in rigor and shape (same "no tools, respond
// with ONLY the array" discipline) — this is a requirements-proposal
// prompt, not a task-decomposition one, so the content differs, but the
// wire contract convention (plain JSON array, no prose) is deliberately
// kept consistent across both LLM-driven Forge features.
const proposeSystemPrompt = `You are a requirements analyst proposing functional (RF) and non-functional (RNF) requirements for a software project, from a short, possibly vague description of the idea.

Do NOT call any tools. Do not explore the filesystem, read files, or run commands — base the proposal ENTIRELY on the idea text given below. Respond immediately with the JSON array — nothing else.

Rules for each item:
- "kind" is "RF" for a functional requirement (something the system DOES — a capability, an action a user or the system takes) or "RNF" for a non-functional one (a quality or constraint the system must satisfy — performance, security, data persistence, availability, usability, etc.).
- "text" is one clear, specific sentence — specific enough that someone could later check whether it was met. Avoid vague filler like "the system should be user-friendly" or "the system should be fast" with no further detail.
- Propose 4-8 RF items and 2-4 RNF items for a first pass — enough to be genuinely useful, not an exhaustive specification. The user can ask for more later.
- Order RF items roughly by how central they are to the idea — the most obviously necessary capability first.
- Do not restate the idea itself as a requirement; every item must describe something NEW the idea implies, not paraphrase the prompt.
- If items are supplied under ALREADY PROPOSED below, propose only items not already covered there — do not repeat or trivially reword an existing one.

Respond with ONLY a JSON array, no prose before or after, no markdown code fences. Each element:
{"kind": "RF" or "RNF", "text": "..."}
`

// proposedItem mirrors the model's per-item JSON shape. Kept separate from
// Item (the session's own type, which also carries Index/ID/Status —
// fields the model never produces) so the wire contract stays explicit and
// decoupled from any future rename of Item's fields.
type proposedItem struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// BuildProposePrompt renders the full user-turn text for a proposal call:
// proposeSystemPrompt followed by the idea and, when existing is non-empty,
// the already-decided items so a repeat call (a later phase's
// "suggest more") doesn't repropose or contradict them. existing is nil
// for the very first call (Fase 1's Start).
func BuildProposePrompt(idea string, existing []Item) string {
	var sb strings.Builder
	sb.WriteString(proposeSystemPrompt)
	sb.WriteString("\n\nIDEA:\n")
	sb.WriteString(strings.TrimSpace(idea))
	if len(existing) > 0 {
		sb.WriteString("\n\nALREADY PROPOSED (do not repeat these, propose only NEW items):\n")
		for _, it := range existing {
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", it.Status, it.ID, it.Text))
		}
	}
	return sb.String()
}

// ParseProposedItems extracts []proposedItem from a model's raw text
// response, using the same progressively looser extraction as
// internal/run.ParseDecomposedTasks (internal/llmjson.Candidates) — models
// routinely ignore "no prose/no fences" instructions, so this tries the
// raw text, then fence-stripped, then the outermost bracketed array
// substring, rather than failing on the first non-strict-JSON response.
func ParseProposedItems(raw string) ([]proposedItem, error) {
	var lastErr error
	for _, c := range llmjson.Candidates(raw) {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		var decoded []proposedItem
		if err := json.Unmarshal([]byte(c), &decoded); err != nil {
			lastErr = err
			continue
		}
		if len(decoded) == 0 {
			lastErr = fmt.Errorf("decoded an empty item list")
			continue
		}
		for i, d := range decoded {
			if d.Kind != string(KindRF) && d.Kind != string(KindRNF) {
				lastErr = fmt.Errorf("item %d has invalid kind %q (want %q or %q)", i, d.Kind, KindRF, KindRNF)
				decoded = nil
				break
			}
			if strings.TrimSpace(d.Text) == "" {
				lastErr = fmt.Errorf("item %d has empty text", i)
				decoded = nil
				break
			}
		}
		if decoded == nil {
			continue
		}
		return decoded, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no JSON array found in response")
	}
	return nil, fmt.Errorf("could not parse a proposed RF/RNF list from the response: %w", lastErr)
}
