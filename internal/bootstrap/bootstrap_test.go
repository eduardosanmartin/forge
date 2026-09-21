package bootstrap

import (
	"context"
	"errors"
	"testing"
)

func fakeProposer(response string, err error) Proposer {
	return func(ctx context.Context, prompt string) (string, error) {
		return response, err
	}
}

func TestManager_Start_HappyPath(t *testing.T) {
	resp := `[
		{"kind": "RF", "text": "Registrar un gasto con monto, quien pago, y entre quienes se divide"},
		{"kind": "RF", "text": "Calcular el balance neto de cada persona"},
		{"kind": "RNF", "text": "Los datos persisten localmente"}
	]`
	m := NewManager(fakeProposer(resp, nil))

	st, err := m.Start(context.Background(), "una app que trackea gastos compartidos entre roommates")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st.ID == "" {
		t.Fatal("expected a non-empty session id")
	}
	if len(st.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(st.Items))
	}

	// Index is 1..N sequential across kinds; ID numbers each kind
	// independently starting at 1 — exactly the mockup's RF-1/RF-2/RNF-1
	// shape (hojaDeRuta-wizard-inteligente.md).
	want := []struct {
		index int
		kind  Kind
		id    string
	}{
		{1, KindRF, "RF-1"},
		{2, KindRF, "RF-2"},
		{3, KindRNF, "RNF-1"},
	}
	for i, w := range want {
		got := st.Items[i]
		if got.Index != w.index || got.Kind != w.kind || got.ID != w.id {
			t.Errorf("item %d = %+v, want index=%d kind=%s id=%s", i, got, w.index, w.kind, w.id)
		}
		if got.Status != StatusPending {
			t.Errorf("item %d status = %q, want %q (nothing is decided yet)", i, got.Status, StatusPending)
		}
	}
}

func TestManager_Start_ProposerError(t *testing.T) {
	m := NewManager(fakeProposer("", errors.New("upstream boom")))
	if _, err := m.Start(context.Background(), "idea"); err == nil {
		t.Fatal("expected an error when the proposer itself fails")
	}
}

func TestManager_Start_UnparseableResponse(t *testing.T) {
	m := NewManager(fakeProposer("this is not json at all", nil))
	if _, err := m.Start(context.Background(), "idea"); err == nil {
		t.Fatal("expected an error for a response with no JSON array")
	}
}

func TestManager_Start_InvalidKindRejected(t *testing.T) {
	// A model ignoring the prompt's format instructions (e.g. "functional"
	// instead of "RF") must fail loudly, not silently corrupt numbering.
	resp := `[{"kind": "functional", "text": "something"}]`
	m := NewManager(fakeProposer(resp, nil))
	if _, err := m.Start(context.Background(), "idea"); err == nil {
		t.Fatal("expected an error for an invalid kind")
	}
}

func TestManager_Start_TripleFallbackParsing(t *testing.T) {
	// The model wraps the array in a code fence AND adds trailing prose —
	// both extraction fallbacks (llmjson) need to kick in together.
	resp := "```json\n[{\"kind\": \"RF\", \"text\": \"hacer algo\"}]\n```\nHope this helps!"
	m := NewManager(fakeProposer(resp, nil))
	st, err := m.Start(context.Background(), "idea")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(st.Items) != 1 || st.Items[0].ID != "RF-1" {
		t.Fatalf("got %+v, want one RF-1 item", st.Items)
	}
}

func TestManager_Status_UnknownSessionErrors(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil))
	if _, err := m.Status("does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

func TestManager_Status_ReturnsCurrentState(t *testing.T) {
	resp := `[{"kind": "RF", "text": "algo"}]`
	m := NewManager(fakeProposer(resp, nil))
	st, err := m.Start(context.Background(), "idea original")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := m.Status(st.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Idea != "idea original" {
		t.Errorf("Idea = %q, want %q", got.Idea, "idea original")
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(got.Items))
	}
}

// TestState_Snapshot_IsADefensiveCopy locks in that mutating a snapshot
// returned to a caller (e.g. a daemon RPC handler serializing it to JSON)
// can never corrupt the Manager's own internal state.
func TestState_Snapshot_IsADefensiveCopy(t *testing.T) {
	resp := `[{"kind": "RF", "text": "algo"}]`
	m := NewManager(fakeProposer(resp, nil))
	st, err := m.Start(context.Background(), "idea")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	st.Items[0].Text = "corrupted by the caller"

	fresh, err := m.Status(st.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if fresh.Items[0].Text != "algo" {
		t.Fatalf("Manager's internal state was mutated through a returned snapshot: got %q", fresh.Items[0].Text)
	}
}
