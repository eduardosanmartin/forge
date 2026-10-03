package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/tools"
)

// Permission "ask" (F1): an operation matching a permissions.*.ask rule is
// held while every connected client is asked; the first answer wins. No
// connected client, a timeout, or a cancelled turn all resolve to deny —
// "ask" never weakens deny-by-default.
const (
	MethodPermissionRequestEvent  = "permission.request.event"
	MethodPermissionResolvedEvent = "permission.resolved.event"
	MethodPermissionRespond       = "permission.respond"
	MethodPermissionPending       = "permission.pending"

	// DefaultPermissionAskTimeout bounds how long an operation waits for a
	// human before it is denied.
	DefaultPermissionAskTimeout = 5 * time.Minute
)

// PermissionRequestPayload is the permission.request.event payload, also
// listed by permission.pending.
type PermissionRequestPayload struct {
	RequestID string `json:"request_id"`
	tools.AskRequest
	CreatedAt int64 `json:"created_at"`
	ExpiresAt int64 `json:"expires_at"`
}

// PermissionResolvedPayload tells every client a request was answered (or
// expired), so other prompts for it can close.
type PermissionResolvedPayload struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

// PermissionRespondParams for permission.respond.
type PermissionRespondParams struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"` // allow_once | allow_session | deny
}

// PermissionPendingResult for permission.pending.
type PermissionPendingResult struct {
	Requests []PermissionRequestPayload `json:"requests"`
}

var errPermissionRequestNotFound = errors.New("permission request not found (already answered or expired)")

type pendingAsk struct {
	payload PermissionRequestPayload
	answer  chan tools.AskDecision
}

// permissionBroker implements tools.Asker over JSON-RPC notifications.
type permissionBroker struct {
	mu         sync.Mutex
	pending    map[string]*pendingAsk
	publish    func(*JSONRPCNotification)
	hasClients func() bool
	timeout    time.Duration
}

func newPermissionBroker(publish func(*JSONRPCNotification), hasClients func() bool) *permissionBroker {
	return &permissionBroker{
		pending:    make(map[string]*pendingAsk),
		publish:    publish,
		hasClients: hasClients,
		timeout:    DefaultPermissionAskTimeout,
	}
}

// Ask is the tools.Asker.
func (b *permissionBroker) Ask(ctx context.Context, req tools.AskRequest) tools.AskDecision {
	if b.hasClients != nil && !b.hasClients() {
		return tools.AskDeny
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return tools.AskDeny
	}
	now := time.Now()
	p := &pendingAsk{
		payload: PermissionRequestPayload{
			RequestID:  hex.EncodeToString(raw),
			AskRequest: req,
			CreatedAt:  now.UnixMilli(),
			ExpiresAt:  now.Add(b.timeout).UnixMilli(),
		},
		answer: make(chan tools.AskDecision, 1),
	}
	b.mu.Lock()
	b.pending[p.payload.RequestID] = p
	b.mu.Unlock()
	b.notify(MethodPermissionRequestEvent, p.payload)

	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	decision := tools.AskDeny
	select {
	case decision = <-p.answer:
	case <-timer.C:
	case <-ctx.Done():
	}
	b.mu.Lock()
	delete(b.pending, p.payload.RequestID)
	b.mu.Unlock()
	b.notify(MethodPermissionResolvedEvent, PermissionResolvedPayload{RequestID: p.payload.RequestID, Decision: string(decision)})
	return decision
}

// Respond delivers a human's answer to a pending request.
func (b *permissionBroker) Respond(id, decision string) error {
	var d tools.AskDecision
	switch tools.AskDecision(decision) {
	case tools.AskAllowOnce, tools.AskAllowSession, tools.AskDeny:
		d = tools.AskDecision(decision)
	default:
		return fmt.Errorf("decision must be allow_once, allow_session or deny, got %q", decision)
	}
	b.mu.Lock()
	p, ok := b.pending[id]
	if ok {
		delete(b.pending, id) // first answer wins
	}
	b.mu.Unlock()
	if !ok {
		return errPermissionRequestNotFound
	}
	p.answer <- d
	return nil
}

// Pending lists unanswered requests, oldest first (for clients that
// connect after the event was sent).
func (b *permissionBroker) Pending() []PermissionRequestPayload {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]PermissionRequestPayload, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p.payload)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (b *permissionBroker) notify(method string, payload any) {
	if b.publish == nil {
		return
	}
	if n, err := NewNotification(method, payload); err == nil {
		b.publish(n)
	}
}
