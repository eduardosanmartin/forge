package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/run"
)

func fakeDecomposer(tasks []run.Task, err error) run.Decomposer {
	return func(ctx context.Context, goal, spec string) ([]run.Task, error) {
		return tasks, err
	}
}

func startAcceptTwo(t *testing.T) (*Manager, string) {
	t.Helper()
	resp := `[
		{"kind": "RF", "text": "Registrar un gasto"},
		{"kind": "RF", "text": "Calcular el balance"},
		{"kind": "RNF", "text": "Persistencia local"}
	]`
	m := NewManager(fakeProposer(resp, nil))
	st, err := m.Start(context.Background(), "una app de gastos compartidos")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Select(st.ID, []int{1, 3}); err != nil { // RF-1 and RNF-1, leave RF-2 pending
		t.Fatalf("Select: %v", err)
	}
	return m, st.ID
}

func TestManager_Finalize_HappyPath(t *testing.T) {
	m, id := startAcceptTwo(t)
	m.decompose = fakeDecomposer([]run.Task{{ID: "t1", Goal: "hacer algo"}}, nil)

	art, err := m.Finalize(context.Background(), id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if art.Manifest == nil || art.Config == nil || art.SpecMD == "" {
		t.Fatalf("incomplete artifacts: %+v", art)
	}
	if len(art.Manifest.Tasks) != 1 || art.Manifest.Tasks[0].ID != "t1" {
		t.Errorf("Manifest.Tasks = %+v, want the decomposer's tasks", art.Manifest.Tasks)
	}
	if art.Manifest.SpecRef != "SPEC.md" {
		t.Errorf("Manifest.SpecRef = %q, want SPEC.md", art.Manifest.SpecRef)
	}
	if art.Manifest.Goal == "" {
		t.Error("Manifest.Goal is empty")
	}

	// SPEC.md must include the accepted items (RF-1, RNF-1) and NOT the
	// still-pending one (RF-2).
	if !strings.Contains(art.SpecMD, "RF-1") || !strings.Contains(art.SpecMD, "Registrar un gasto") {
		t.Errorf("SPEC.md missing accepted RF-1: %q", art.SpecMD)
	}
	if !strings.Contains(art.SpecMD, "RNF-1") || !strings.Contains(art.SpecMD, "Persistencia local") {
		t.Errorf("SPEC.md missing accepted RNF-1: %q", art.SpecMD)
	}
	if strings.Contains(art.SpecMD, "Calcular el balance") {
		t.Errorf("SPEC.md included a still-pending item: %q", art.SpecMD)
	}

	st, err := m.Status(id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Finalized {
		t.Error("session not marked Finalized after a successful Finalize")
	}
}

func TestManager_Finalize_NoAcceptedItems(t *testing.T) {
	resp := `[{"kind": "RF", "text": "algo"}]`
	m := NewManager(fakeProposer(resp, nil), WithDecomposer(fakeDecomposer(nil, nil)))
	st, err := m.Start(context.Background(), "idea")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := m.Finalize(context.Background(), st.ID); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Finalize error = %v, want ErrInvalidRequest", err)
	}
}

func TestManager_Finalize_DiscardedItemsExcluded(t *testing.T) {
	resp := `[{"kind": "RF", "text": "aceptado"}, {"kind": "RF", "text": "descartado"}]`
	m := NewManager(fakeProposer(resp, nil), WithDecomposer(fakeDecomposer(nil, nil)))
	st, err := m.Start(context.Background(), "idea")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Select(st.ID, []int{1}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, err := m.Discard(st.ID, []int{2}); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	art, err := m.Finalize(context.Background(), st.ID)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if strings.Contains(art.SpecMD, "descartado") {
		t.Errorf("SPEC.md included a discarded item: %q", art.SpecMD)
	}
	if !strings.Contains(art.SpecMD, "aceptado") {
		t.Errorf("SPEC.md missing the accepted item: %q", art.SpecMD)
	}
}

func TestManager_Finalize_DecomposerNotConfigured(t *testing.T) {
	m, id := startAcceptTwo(t) // no WithDecomposer passed to NewManager, m.decompose left nil
	if _, err := m.Finalize(context.Background(), id); err == nil {
		t.Fatal("expected an error when no Decomposer is configured")
	}
}

func TestManager_Finalize_DecomposerError(t *testing.T) {
	m, id := startAcceptTwo(t)
	m.decompose = fakeDecomposer(nil, errors.New("upstream boom"))

	if _, err := m.Finalize(context.Background(), id); err == nil {
		t.Fatal("expected an error when the decomposer fails")
	}
}

func TestManager_Finalize_UnknownSession(t *testing.T) {
	m := NewManager(fakeProposer("[]", nil), WithDecomposer(fakeDecomposer(nil, nil)))
	if _, err := m.Finalize(context.Background(), "does-not-exist"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Finalize error = %v, want ErrSessionNotFound", err)
	}
}

func TestSlugFromIdea(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Una app de gastos compartidos", "una-app-de-gastos-compartidos"},
		{"  ¡Hola, Mundo!  ", "hola-mundo"},
		{"", "proyecto"},
		{"---", "proyecto"},
	}
	for _, c := range cases {
		if got := slugFromIdea(c.in); got != c.want {
			t.Errorf("slugFromIdea(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
