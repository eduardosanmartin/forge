package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sequenceProposer returns each of responses in order across successive
// calls (one call per Start/SuggestMore round), recording every prompt it
// was given into *prompts so a test can assert on what context a later
// round actually sent.
func sequenceProposer(t *testing.T, prompts *[]string, responses ...string) Proposer {
	t.Helper()
	i := 0
	return func(ctx context.Context, prompt string) (string, error) {
		if prompts != nil {
			*prompts = append(*prompts, prompt)
		}
		if i >= len(responses) {
			t.Fatalf("proposer called more times (%d) than responses provided (%d)", i+1, len(responses))
		}
		resp := responses[i]
		i++
		return resp, nil
	}
}

func startWithTwoRF(t *testing.T) *Manager {
	t.Helper()
	resp := `[
		{"kind": "RF", "text": "Registrar un gasto"},
		{"kind": "RF", "text": "Calcular el balance"}
	]`
	m := NewManager(fakeProposer(resp, nil))
	if _, err := m.Start(context.Background(), "idea"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m
}

func firstSessionID(t *testing.T, m *Manager) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.sessions {
		return id
	}
	t.Fatal("no session found")
	return ""
}

func TestManager_Select_HappyPath(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	st, err := m.Select(id, []int{1})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if st.Items[0].Status != StatusAccepted {
		t.Errorf("item 1 status = %q, want %q", st.Items[0].Status, StatusAccepted)
	}
	if st.Items[1].Status != StatusPending {
		t.Errorf("item 2 status = %q, want %q (untouched)", st.Items[1].Status, StatusPending)
	}
}

func TestManager_Discard_HappyPath(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	st, err := m.Discard(id, []int{2})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if st.Items[1].Status != StatusDiscarded {
		t.Errorf("item 2 status = %q, want %q", st.Items[1].Status, StatusDiscarded)
	}
}

func TestManager_Select_ThenDiscard_Redecide(t *testing.T) {
	// Nothing is final until Finalize (a later phase) — the user can change
	// their mind on an already-decided item before saying "listo".
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	if _, err := m.Select(id, []int{1}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	st, err := m.Discard(id, []int{1})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if st.Items[0].Status != StatusDiscarded {
		t.Errorf("item 1 status = %q, want %q after redeciding", st.Items[0].Status, StatusDiscarded)
	}
}

func TestManager_Select_EmptyIndices(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	if _, err := m.Select(id, nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Select(nil) error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_Select_UnknownIndex_MutatesNothing(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	_, err := m.Select(id, []int{1, 99})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Select error = %v, want ErrInvalidRequest", err)
	}

	st, statusErr := m.Status(id)
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if st.Items[0].Status != StatusPending {
		t.Errorf("item 1 status = %q, want %q (a bad index in the same call must not partially apply)", st.Items[0].Status, StatusPending)
	}
}

func TestManager_Select_UnknownSession(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil))
	if _, err := m.Select("does-not-exist", []int{1}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Select error = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_SuggestOwn_HappyPath(t *testing.T) {
	m := startWithTwoRF(t) // RF-1, RF-2 already assigned
	id := firstSessionID(t, m)

	st, err := m.SuggestOwn(id, KindRNF, "Debe funcionar sin conexion a internet")
	if err != nil {
		t.Fatalf("SuggestOwn: %v", err)
	}
	if len(st.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(st.Items))
	}
	last := st.Items[2]
	if last.Kind != KindRNF || last.ID != "RNF-1" || last.Status != StatusPending {
		t.Errorf("suggested item = %+v, want kind=RNF id=RNF-1 status=pending", last)
	}
	if last.Index != 3 {
		t.Errorf("suggested item Index = %d, want 3 (continuing the existing sequence)", last.Index)
	}
}

func TestManager_SuggestOwn_InvalidKind(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	if _, err := m.SuggestOwn(id, Kind("functional"), "algo"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SuggestOwn error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_SuggestOwn_EmptyText(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)

	if _, err := m.SuggestOwn(id, KindRF, "   "); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SuggestOwn error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_SuggestOwn_UnknownSession(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil))
	if _, err := m.SuggestOwn("does-not-exist", KindRF, "algo"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SuggestOwn error = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_SuggestMore_AppendsAndContinuesNumbering(t *testing.T) {
	var prompts []string
	firstResp := `[{"kind": "RF", "text": "Registrar un gasto"}]`
	secondResp := `[{"kind": "RF", "text": "Historial filtrable"}, {"kind": "RNF", "text": "Persistencia local"}]`
	m := NewManager(sequenceProposer(t, &prompts, firstResp, secondResp))

	st, err := m.Start(context.Background(), "una app de gastos compartidos")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	st, err = m.SuggestMore(context.Background(), st.ID)
	if err != nil {
		t.Fatalf("SuggestMore: %v", err)
	}
	if len(st.Items) != 3 {
		t.Fatalf("got %d items, want 3 (1 original + 2 suggested)", len(st.Items))
	}
	if st.Items[0].ID != "RF-1" || st.Items[1].ID != "RF-2" || st.Items[2].ID != "RNF-1" {
		t.Fatalf("unexpected ids after SuggestMore: %+v", st.Items)
	}
	if st.Items[0].Index != 1 || st.Items[1].Index != 2 || st.Items[2].Index != 3 {
		t.Fatalf("unexpected indices after SuggestMore: %+v", st.Items)
	}
}

func TestManager_SuggestMore_PromptCarriesExistingItems(t *testing.T) {
	// UX decision #4 (hojaDeRuta-wizard-inteligente.md): a "+" round must
	// see what's already proposed/decided so it doesn't repeat or
	// contradict it — verify the prompt actually carries that context.
	var prompts []string
	firstResp := `[{"kind": "RF", "text": "Registrar un gasto"}]`
	secondResp := `[{"kind": "RNF", "text": "Persistencia local"}]`
	m := NewManager(sequenceProposer(t, &prompts, firstResp, secondResp))

	st, err := m.Start(context.Background(), "idea")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.SuggestMore(context.Background(), st.ID); err != nil {
		t.Fatalf("SuggestMore: %v", err)
	}

	if len(prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "ALREADY PROPOSED") || !strings.Contains(prompts[1], "Registrar un gasto") {
		t.Errorf("second prompt did not carry the first round's items: %q", prompts[1])
	}
}

func TestManager_SuggestMore_ProposerError(t *testing.T) {
	m := startWithTwoRF(t)
	id := firstSessionID(t, m)
	m.propose = fakeProposer("", errors.New("upstream boom"))

	if _, err := m.SuggestMore(context.Background(), id); err == nil {
		t.Fatal("expected an error when the proposer fails")
	}
}

func TestManager_SuggestMore_UnknownSession(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil))
	if _, err := m.SuggestMore(context.Background(), "does-not-exist"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SuggestMore error = %v, want ErrSessionNotFound", err)
	}
}
