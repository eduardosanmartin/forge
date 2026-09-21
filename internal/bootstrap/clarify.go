package bootstrap

import (
	"fmt"
	"strings"
)

// clarifySystemPrompt frames a clarification sub-chat about ONE proposed
// item (the "? N" command). Sent only on the sub-chat's opening question —
// every later turn in the same daemon session already carries this framing
// in its own message history (see Manager.Clarify), so it sends just the
// raw follow-up question.
const clarifySystemPrompt = `You are helping a user decide whether to accept, discard, or reword ONE specific proposed requirement, as part of a larger requirements-gathering conversation for a software project idea.

Do NOT call any tools. Do not propose new requirements, do not restate the whole idea, do not suggest changes to other items — answer ONLY the question asked, grounded in the idea and the item below. Be concise: a few sentences, not an essay. If the question can't be answered from the given context, say so plainly rather than guessing.
`

// BuildClarifyPrompt renders the user-turn text for one clarify call.
// firstTurn selects whether the full framing (system prompt + idea + item
// context) is included: true for a sub-chat's opening question, false for
// a follow-up in the same daemon session, whose message history already
// carries that framing from turn one.
func BuildClarifyPrompt(idea string, item Item, question string, firstTurn bool) string {
	question = strings.TrimSpace(question)
	if !firstTurn {
		return question
	}
	var sb strings.Builder
	sb.WriteString(clarifySystemPrompt)
	sb.WriteString("\nIDEA:\n")
	sb.WriteString(strings.TrimSpace(idea))
	sb.WriteString(fmt.Sprintf("\n\nITEM (%s): %s\n", item.ID, item.Text))
	sb.WriteString("\nQUESTION:\n")
	sb.WriteString(question)
	return sb.String()
}
