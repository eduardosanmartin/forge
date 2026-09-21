// Package bootstrap implements the intelligent wizard loop
// (hojaDeRuta-wizard-inteligente.md): turning a short, possibly vague
// project idea into a proposed, user-curated set of RF/RNF requirements,
// which Fase 4 later turns into SPEC.md, .forge/config.json, and run.json.
//
// Fase 1 scope only: the initial proposal (Start) and reading state back
// (Status). Selection, suggestions, clarification, and finalization are
// separate phases layered on top of the same State — see the roadmap.
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// Kind distinguishes a functional requirement from a non-functional one.
type Kind string

const (
	KindRF  Kind = "RF"
	KindRNF Kind = "RNF"
)

// Status tracks the user's decision on a proposed item. Every item starts
// Pending; later phases (Select) move it to Accepted or Discarded.
type Status string

const (
	StatusPending   Status = "pending"
	StatusAccepted  Status = "accepted"
	StatusDiscarded Status = "discarded"
)

// Item is one proposed (model-generated or, in a later phase, user-typed)
// RF/RNF candidate. Index is the item's stable position in the session's
// flat, ever-growing list — assigned once at creation and never reused or
// renumbered (see hojaDeRuta-wizard-inteligente.md's UX decision #1: the
// numbering must survive a round trip through the clarification sub-chat
// unchanged). ID is the human-facing requirement label within its own
// Kind's numbering (e.g. "RF-3"), independent of Index.
type Item struct {
	Index  int    `json:"index"`
	Kind   Kind   `json:"kind"`
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status Status `json:"status"`
}

// State is one in-progress bootstrap session. Held in memory only — see
// the roadmap's "Estado intermedio" decision: a session lost to a daemon
// restart before Finalize is a minor inconvenience (nothing has been
// written to disk yet), not real data loss, and isn't worth the
// complexity of persisting an interactive, single-sitting conversation.
type State struct {
	ID        string
	Idea      string
	Items     []Item
	Finalized bool

	nextIndex int
	nextRFNum int
	nextRNFNum int
}

// snapshot returns a defensive copy of the state safe to hand to a caller
// outside the Manager's lock (Items is a slice — copying the header alone
// would still let a caller mutate the backing array).
func (s *State) snapshot() *State {
	cp := *s
	cp.Items = make([]Item, len(s.Items))
	copy(cp.Items, s.Items)
	return &cp
}

// appendProposed assigns Index/ID/Status to each newly proposed item (in
// the order given) and appends them to Items. Kind values other than
// KindRF/KindRNF are rejected — a model that ignores the prompt's format
// instructions should fail loudly here, not silently corrupt numbering.
func (s *State) appendProposed(proposed []proposedItem) error {
	for _, p := range proposed {
		kind := Kind(p.Kind)
		var id string
		switch kind {
		case KindRF:
			s.nextRFNum++
			id = fmt.Sprintf("RF-%d", s.nextRFNum)
		case KindRNF:
			s.nextRNFNum++
			id = fmt.Sprintf("RNF-%d", s.nextRNFNum)
		default:
			return fmt.Errorf("proposed item has invalid kind %q (want %q or %q)", p.Kind, KindRF, KindRNF)
		}
		s.nextIndex++
		s.Items = append(s.Items, Item{
			Index:  s.nextIndex,
			Kind:   kind,
			ID:     id,
			Text:   p.Text,
			Status: StatusPending,
		})
	}
	return nil
}

// Proposer calls an LLM with prompt and returns its raw text response.
// The daemon wires this to a throwaway no-tools session's ExecuteTurn —
// see internal/daemon/runs.go's manifestDecomposer for the pattern this
// mirrors (RF-11's task decomposition), and internal/daemon's own
// bootstrap RPC handlers (Fase 1's daemon-side wiring) for the real
// implementation. Kept as an interface here so Manager's own logic is
// testable without a daemon, a session manager, or a real LLM.
type Proposer func(ctx context.Context, prompt string) (string, error)

// Manager holds every in-progress bootstrap session for one daemon
// process. Safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*State
	propose  Proposer
}

// NewManager creates a Manager backed by propose for every LLM call.
func NewManager(propose Proposer) *Manager {
	return &Manager{
		sessions: make(map[string]*State),
		propose:  propose,
	}
}

// Start begins a new bootstrap session from idea: one LLM call proposing
// an initial RF/RNF set, stored under a fresh session ID.
func (m *Manager) Start(ctx context.Context, idea string) (*State, error) {
	prompt := BuildProposePrompt(idea, nil)
	raw, err := m.propose(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("propose: %w", err)
	}
	proposed, err := ParseProposedItems(raw)
	if err != nil {
		return nil, fmt.Errorf("parse proposal: %w", err)
	}

	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	st := &State{ID: id, Idea: idea}
	if err := st.appendProposed(proposed); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[id] = st
	m.mu.Unlock()

	return st.snapshot(), nil
}

// Status returns the current state of an existing session.
func (m *Manager) Status(id string) (*State, error) {
	m.mu.Lock()
	st, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("bootstrap session %q not found", id)
	}
	return st.snapshot(), nil
}

// newSessionID returns a random 16-hex-char (8-byte) identifier — enough
// entropy to make collision practically impossible for a small number of
// concurrent in-memory sessions, without needing a full UUID dependency.
func newSessionID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
