package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingClarifier fakes a real multi-turn LLM: each call is keyed by the
// sessionID it receives, so it can tell a fresh sub-chat (sessionID=="")
// apart from a follow-up in an existing one, and records the prompt it saw
// for each session so a test can assert what context a follow-up actually
// carried.
type recordingClarifier struct {
	nextSessionNum int
	promptsBySID   map[string][]string
	answer         string
	err            error
}

func newRecordingClarifier(answer string) *recordingClarifier {
	return &recordingClarifier{promptsBySID: make(map[string][]string), answer: answer}
}

func (c *recordingClarifier) Clarify(ctx context.Context, sessionID, prompt string) (string, string, error) {
	if c.err != nil {
		return "", "", c.err
	}
	sid := sessionID
	if sid == "" {
		c.nextSessionNum++
		sid = "clarify-session-" + string(rune('a'+c.nextSessionNum-1))
	}
	c.promptsBySID[sid] = append(c.promptsBySID[sid], prompt)
	return c.answer, sid, nil
}

func TestManager_Clarify_HappyPath(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("RF-1 asume un solo grupo por gasto.")
	m.clarify = rc.Clarify

	answer, err := m.Clarify(context.Background(), id, 1, "¿Puede un gasto pertenecer a mas de un grupo?")
	if err != nil {
		t.Fatalf("Clarify: %v", err)
	}
	if answer != "RF-1 asume un solo grupo por gasto." {
		t.Errorf("answer = %q", answer)
	}
}

func TestManager_Clarify_FirstTurnPromptCarriesFraming(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("respuesta")
	m.clarify = rc.Clarify

	question := "¿Puede un gasto pertenecer a mas de un grupo?"
	if _, err := m.Clarify(context.Background(), id, 1, question); err != nil {
		t.Fatalf("Clarify: %v", err)
	}

	if len(rc.promptsBySID) != 1 {
		t.Fatalf("got %d distinct sessions, want 1", len(rc.promptsBySID))
	}
	for sid, prompts := range rc.promptsBySID {
		if len(prompts) != 1 {
			t.Fatalf("session %q got %d prompts, want 1", sid, len(prompts))
		}
		if !strings.Contains(prompts[0], "IDEA:") || !strings.Contains(prompts[0], "RF-1") || !strings.Contains(prompts[0], question) {
			t.Errorf("first-turn prompt missing expected framing: %q", prompts[0])
		}
	}
}

func TestManager_Clarify_FollowUpReusesSessionAndSkipsFraming(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("respuesta")
	m.clarify = rc.Clarify

	if _, err := m.Clarify(context.Background(), id, 1, "primera pregunta"); err != nil {
		t.Fatalf("Clarify (turn 1): %v", err)
	}
	if _, err := m.Clarify(context.Background(), id, 1, "segunda pregunta"); err != nil {
		t.Fatalf("Clarify (turn 2): %v", err)
	}

	if len(rc.promptsBySID) != 1 {
		t.Fatalf("got %d distinct sessions across two turns on the same item, want 1 (the sub-chat must reuse its session)", len(rc.promptsBySID))
	}
	for _, prompts := range rc.promptsBySID {
		if len(prompts) != 2 {
			t.Fatalf("got %d prompts in the reused session, want 2", len(prompts))
		}
		if strings.Contains(prompts[1], "IDEA:") {
			t.Errorf("follow-up prompt re-sent the full framing instead of just the question: %q", prompts[1])
		}
		if prompts[1] != "segunda pregunta" {
			t.Errorf("follow-up prompt = %q, want just the raw question", prompts[1])
		}
	}
}

func TestManager_Clarify_DifferentItemsGetDifferentSessions(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("respuesta")
	m.clarify = rc.Clarify

	if _, err := m.Clarify(context.Background(), id, 1, "pregunta sobre RF-1"); err != nil {
		t.Fatalf("Clarify item 1: %v", err)
	}
	if _, err := m.Clarify(context.Background(), id, 2, "pregunta sobre RF-2"); err != nil {
		t.Fatalf("Clarify item 2: %v", err)
	}

	if len(rc.promptsBySID) != 2 {
		t.Fatalf("got %d distinct sessions across two different items, want 2", len(rc.promptsBySID))
	}
}

func TestManager_Clarify_DoesNotMutateItems(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("respuesta")
	m.clarify = rc.Clarify

	before, err := m.Status(id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if _, err := m.Clarify(context.Background(), id, 1, "una pregunta"); err != nil {
		t.Fatalf("Clarify: %v", err)
	}
	after, err := m.Status(id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(after.Items) != len(before.Items) {
		t.Fatalf("item count changed: before=%d after=%d", len(before.Items), len(after.Items))
	}
	for i := range before.Items {
		if before.Items[i] != after.Items[i] {
			t.Errorf("item %d changed: before=%+v after=%+v", i, before.Items[i], after.Items[i])
		}
	}
}

func TestManager_Clarify_EmptyQuestion(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	m.clarify = newRecordingClarifier("x").Clarify

	if _, err := m.Clarify(context.Background(), id, 1, "   "); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Clarify error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_Clarify_UnknownIndex(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	m.clarify = newRecordingClarifier("x").Clarify

	if _, err := m.Clarify(context.Background(), id, 99, "pregunta"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Clarify error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_Clarify_UnknownSession(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil), WithClarifier(newRecordingClarifier("x").Clarify))
	if _, err := m.Clarify(context.Background(), "does-not-exist", 1, "pregunta"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Clarify error = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_Clarify_NotConfigured(t *testing.T) {
	// No WithClarifier option: Start/Select/etc. must keep working, only
	// Clarify itself needs one.
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	if _, err := m.Clarify(context.Background(), id, 1, "pregunta"); err == nil {
		t.Fatal("expected an error when no Clarifier is configured")
	}
}

func TestManager_Clarify_ClarifierError(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	rc := newRecordingClarifier("")
	rc.err = errors.New("upstream boom")
	m.clarify = rc.Clarify

	if _, err := m.Clarify(context.Background(), id, 1, "pregunta"); err == nil {
		t.Fatal("expected an error when the clarifier itself fails")
	}
}
