// Package bootstrap implements the intelligent wizard loop
// (hojaDeRuta-wizard-inteligente.md): turning a short, possibly vague
// project idea into a proposed, user-curated set of RF/RNF requirements,
// which Fase 4 later turns into SPEC.md, .forge/config.json, and run.json.
//
// Fase 1+2 scope: the initial proposal (Start), reading state back
// (Status), and curating it (Select/Discard/SuggestMore/SuggestOwn).
// Clarification and finalization are separate phases layered on top of the
// same State — see the roadmap.
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrSessionNotFound is wrapped into every "no such bootstrap_id" error, so
// a caller (the daemon's RPC handlers) can map it to a specific error code
// via errors.Is instead of pattern-matching a message string.
var ErrSessionNotFound = errors.New("bootstrap session not found")

// ErrInvalidRequest is wrapped into every error caused by bad caller input
// (empty indices, an unknown item index, a malformed Kind/empty text on a
// user-supplied suggestion) — as opposed to a Proposer/LLM failure, which
// is the caller's problem to retry, not theirs to fix by changing the
// request. Lets the daemon's RPC handlers map this to ErrCodeInvalidParams.
var ErrInvalidRequest = errors.New("invalid bootstrap request")

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

	nextIndex  int
	nextRFNum  int
	nextRNFNum int

	// clarifySessions maps an Item's Index to the daemon session backing
	// its clarification sub-chat (Fase 3's "? N"), if one has been opened.
	// Never exposed to a caller (unexported, not part of the JSON shape) —
	// reusing the same session across follow-up questions about the same
	// item is an internal implementation detail of Manager.Clarify, not
	// something a client needs to see or manage.
	clarifySessions map[int]string
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

// Clarifier answers one question about a specific item, as part of a
// multi-turn sub-chat (Fase 3's "? N"). sessionID is the daemon session
// already backing that item's sub-chat — "" opens a new one — and the
// returned sessionID is what Manager stores to continue that exact
// conversation on the item's next question, so a follow-up gets real
// multi-turn context (the session's own message history) instead of
// Manager re-sending the framing by hand every time. See
// internal/daemon/runs.go's bootstrapClarifier for the real
// implementation; kept as an interface here for the same testability
// reason Proposer is.
type Clarifier func(ctx context.Context, sessionID, prompt string) (answer, newSessionID string, err error)

// Manager holds every in-progress bootstrap session for one daemon
// process. Safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*State
	propose  Proposer
	clarify  Clarifier
}

// Option configures a Manager at construction. Kept as a functional option
// (mirrors daemon.WithV1Deps) so every existing single-argument
// NewManager(propose) call site from Fase 1/2 — production and tests alike
// — keeps compiling unchanged.
type Option func(*Manager)

// WithClarifier wires a Clarifier for Fase 3's bootstrap.clarify. A
// Manager with none configured returns an error from Clarify; the daemon
// always wires one in production (bootstrapClarifier), so this only
// matters to a test that exercises Clarify without supplying one.
func WithClarifier(c Clarifier) Option {
	return func(m *Manager) { m.clarify = c }
}

// NewManager creates a Manager backed by propose for every LLM call.
func NewManager(propose Proposer, opts ...Option) *Manager {
	m := &Manager{
		sessions: make(map[string]*State),
		propose:  propose,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
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
	st, err := m.getSession(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return st.snapshot(), nil
}

// Select marks each item at the given Index as Accepted. Discard does the
// same as Discarded. Re-deciding an already-decided item (accepted →
// discarded or back) is allowed — nothing is final until Finalize (a later
// phase); the user navigates the loop freely until they say "listo".
func (m *Manager) Select(id string, indices []int) (*State, error) {
	return m.setStatus(id, indices, StatusAccepted)
}

func (m *Manager) Discard(id string, indices []int) (*State, error) {
	return m.setStatus(id, indices, StatusDiscarded)
}

// setStatus validates every index exists before changing any of them, so a
// call naming one bad index alongside several good ones mutates nothing
// rather than partially applying.
func (m *Manager) setStatus(id string, indices []int, status Status) (*State, error) {
	if len(indices) == 0 {
		return nil, fmt.Errorf("%w: indices is required", ErrInvalidRequest)
	}
	st, err := m.getSession(id)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	byIndex := make(map[int]*Item, len(st.Items))
	for i := range st.Items {
		byIndex[st.Items[i].Index] = &st.Items[i]
	}
	for _, idx := range indices {
		if _, ok := byIndex[idx]; !ok {
			return nil, fmt.Errorf("%w: item index %d not found", ErrInvalidRequest, idx)
		}
	}
	for _, idx := range indices {
		byIndex[idx].Status = status
	}
	return st.snapshot(), nil
}

// SuggestOwn appends a single user-authored item (the "sugerir: ..."
// command) directly — no LLM call, unlike SuggestMore. kind is validated
// here (not left to appendProposed's own check) because an invalid kind
// here is the CALLER's mistake (ErrInvalidRequest), whereas the same check
// failing inside appendProposed during Start/SuggestMore means the MODEL
// ignored the prompt's format — a different kind of failure, mapped to a
// different RPC error code by the daemon.
func (m *Manager) SuggestOwn(id string, kind Kind, text string) (*State, error) {
	if kind != KindRF && kind != KindRNF {
		return nil, fmt.Errorf("%w: kind must be %q or %q, got %q", ErrInvalidRequest, KindRF, KindRNF, kind)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: text is required", ErrInvalidRequest)
	}
	st, err := m.getSession(id)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := st.appendProposed([]proposedItem{{Kind: string(kind), Text: text}}); err != nil {
		return nil, err
	}
	return st.snapshot(), nil
}

// SuggestMore re-consults the model (the "+" command), sending the idea
// plus every item decided or proposed so far (BuildProposePrompt's
// "ALREADY PROPOSED" section — UX decision #4 in the roadmap: new
// suggestions must not repeat what's accepted or contradict what's
// discarded) and appending whatever comes back.
func (m *Manager) SuggestMore(ctx context.Context, id string) (*State, error) {
	st, err := m.getSession(id)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	idea := st.Idea
	existing := append([]Item(nil), st.Items...)
	m.mu.Unlock()

	prompt := BuildProposePrompt(idea, existing)
	raw, err := m.propose(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("propose: %w", err)
	}
	proposed, err := ParseProposedItems(raw)
	if err != nil {
		return nil, fmt.Errorf("parse proposal: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := st.appendProposed(proposed); err != nil {
		return nil, err
	}
	return st.snapshot(), nil
}

// Clarify answers one question about the item at index, continuing that
// item's own sub-chat if one is already open. Never touches Status, Index,
// or numbering — UX decision #1 in the roadmap requires the selection list
// to come back unchanged after "volver", and Clarify simply never mutates
// Items at all.
func (m *Manager) Clarify(ctx context.Context, id string, index int, question string) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "", fmt.Errorf("%w: question is required", ErrInvalidRequest)
	}
	if m.clarify == nil {
		return "", fmt.Errorf("bootstrap clarify is not configured")
	}
	st, err := m.getSession(id)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	item, ok := itemByIndex(st, index)
	if !ok {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: item index %d not found", ErrInvalidRequest, index)
	}
	idea := st.Idea
	sid := st.clarifySessions[index]
	m.mu.Unlock()

	prompt := BuildClarifyPrompt(idea, item, question, sid == "")
	answer, newSID, err := m.clarify(ctx, sid, prompt)
	if err != nil {
		return "", fmt.Errorf("clarify: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if st.clarifySessions == nil {
		st.clarifySessions = make(map[int]string)
	}
	st.clarifySessions[index] = newSID
	return answer, nil
}

// itemByIndex returns a copy of the item at index (never a pointer into
// st.Items — callers use this to read outside the Manager's lock).
func itemByIndex(st *State, index int) (Item, bool) {
	for _, it := range st.Items {
		if it.Index == index {
			return it, true
		}
	}
	return Item{}, false
}

// getSession looks up an existing session by id, wrapped so every caller
// gets the same ErrSessionNotFound-based error.
func (m *Manager) getSession(id string) (*State, error) {
	m.mu.Lock()
	st, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	return st, nil
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
