package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// longToolTurn returns a chronological transcript: priorTurns complete
// user/assistant exchanges, then a current turn opened by request that ran
// iterations tool round trips (assistant tool call + tool result each).
func longToolTurn(priorTurns, iterations int, request string) []store.Message {
	var out []store.Message
	for i := 0; i < priorTurns; i++ {
		out = append(out,
			store.Message{Role: "user", Content: fmt.Sprintf("old question %d", i)},
			store.Message{Role: "assistant", Content: fmt.Sprintf("old answer %d", i)})
	}
	out = append(out, store.Message{Role: "user", Content: request})
	for i := 0; i < iterations; i++ {
		id := fmt.Sprintf("call-%d", i)
		out = append(out,
			store.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: "fs_read", Arguments: `{"path":"x"}`}}}},
			store.Message{Role: "tool", ToolCallID: id, Name: "fs_read", Content: fmt.Sprintf("result %d", i)})
	}
	return out
}

// C3 regression (2026-10-02 review): with a fixed 16-message window, a turn
// with 12 tool iterations pushed the user's own request out of the context.
func TestSelectHistoryWindow_CurrentTurnNeverCut(t *testing.T) {
	transcript := longToolTurn(5, 12, "refactor the parser")
	window := selectHistoryWindow(transcript, 16)
	if len(window) == 0 || window[0].Role != "user" || window[0].Content != "refactor the parser" {
		t.Fatalf("window must open with the current request, got first=%+v (len %d)", window[0], len(window))
	}
	if got, want := len(window), 1+12*2; got != want {
		t.Errorf("window len = %d, want %d (the whole current turn, no older history left in budget)", got, want)
	}
}

func TestSelectHistoryWindow_OlderHistoryTurnAligned(t *testing.T) {
	transcript := longToolTurn(10, 1, "now")
	window := selectHistoryWindow(transcript, 8) // current turn = 3 messages, 5 left for older
	if window[0].Role != "user" {
		t.Fatalf("window opens with %q, want a user message (whole-turn boundary)", window[0].Role)
	}
	if len(window) > 8 {
		t.Errorf("window len = %d, exceeds the 8-message budget for short turns", len(window))
	}
	if last := window[len(window)-1]; last.Role != "tool" {
		t.Errorf("window must end with the current turn's latest message, got %q", last.Role)
	}
}

func TestSelectHistoryWindow_NoUserMessageDropsOrphanTools(t *testing.T) {
	transcript := []store.Message{{Role: "tool", ToolCallID: "x"}, {Role: "assistant", Content: "a"}}
	window := selectHistoryWindow(transcript, 16)
	if len(window) != 1 || window[0].Role != "assistant" {
		t.Fatalf("window = %+v, want the orphaned tool result dropped", window)
	}
}

// The assembler must page past its first store read to find the current
// turn's request when the turn is longer than one page.
func TestContextAssembler_LongTurnKeepsRequestAcrossPages(t *testing.T) {
	transcript := longToolTurn(3, 40, "the original task") // 81 msgs in the current turn > historyPageSize
	newestFirst := make([]store.Message, len(transcript))
	for i, m := range transcript {
		newestFirst[len(transcript)-1-i] = m
	}
	st := &contextMockStore{session: &store.Session{ID: "s", Metadata: map[string]any{}}, messages: newestFirst}
	asm := NewContextAssembler(tools.New(nil, "", nil), st, 8)

	msgs, err := asm.BuildWithQuery(context.Background(), "s", "", "the original task")
	if err != nil {
		t.Fatalf("BuildWithQuery: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "the original task" {
			found = true
		}
	}
	if !found {
		t.Fatal("the current turn's request is missing from a continuation iteration's context")
	}
}

// Skills used to be injected only when userMessage != "", i.e. only on the
// first iteration of a turn; tool-result continuations lost them.
func TestContextAssembler_SkillsSurviveToolContinuation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "house-style")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: house-style\ndescription: \"house style\"\nsource: local\n---\nHOUSE STYLE BODY\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := skill.NewManager(skill.Options{})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatal(err)
	}

	st := &contextMockStore{session: &store.Session{ID: "s", Metadata: map[string]any{"v1_skills": true}}}
	asm := NewContextAssembler(tools.New(nil, "", nil), st, 8)
	asm.SetV1Deps(V1Deps{Skills: mgr, SkillsLazyLoad: false, SkillsEnabled: []string{"house-style"}})

	msgs, err := asm.BuildWithQuery(context.Background(), "s", "", "fix the bug")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findSystemMessageByPrefix(msgs, "SKILL INSTRUCTIONS (v1)"); !ok {
		t.Fatal("skill instructions missing on a tool-result continuation (userMessage empty, query set)")
	}
}
