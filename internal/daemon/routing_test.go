package daemon

import (
	"testing"

	"github.com/coder/websocket"
)

func routedTo(tr *Transport, cc *ClientConn, n *JSONRPCNotification) int {
	before := len(cc.send)
	tr.dispatchNotification(n)
	return len(cc.send) - before
}

// N5 (review 2026-10-03): subscriptions are session IDs, but dispatch
// compared them against the notification METHOD — a subscribed client got
// none of its own session's events.
func TestSubscribedClientGetsOnlyItsSessionsEvents(t *testing.T) {
	tr := NewTransport("127.0.0.1:0", nil, nil)
	sub := &ClientConn{subscriptions: map[string]bool{"sess-1": true}, send: make(chan []byte, 16)}
	all := &ClientConn{subscriptions: map[string]bool{}, send: make(chan []byte, 16)}
	tr.conns[&websocket.Conn{}] = sub
	tr.conns[&websocket.Conn{}] = all

	own, _ := NewNotification(MethodMessageEvent, map[string]any{"session_id": "sess-1"})
	other, _ := NewNotification(MethodMessageEvent, map[string]any{"session_id": "sess-2"})
	global, _ := NewNotification(MethodPermissionRequestEvent, map[string]any{"request_id": "r"})
	noSession, _ := NewNotification(MethodSessionEvent, map[string]any{"type": "created"})

	if got := routedTo(tr, sub, own); got != 1 {
		t.Errorf("subscribed client: own session event delivered %d times, want 1", got)
	}
	if got := routedTo(tr, sub, other); got != 0 {
		t.Errorf("subscribed client: another session's event delivered %d times, want 0", got)
	}
	if got := routedTo(tr, sub, global); got != 1 {
		t.Errorf("subscribed client: global event delivered %d times, want 1", got)
	}
	if got := routedTo(tr, sub, noSession); got != 1 {
		t.Errorf("subscribed client: event without session_id delivered %d times, want 1", got)
	}
	// A client with no subscriptions keeps receiving everything (today's
	// clients never subscribe — behavior unchanged for them).
	before := len(all.send)
	for _, n := range []*JSONRPCNotification{own, other, global, noSession} {
		tr.dispatchNotification(n)
	}
	if got := len(all.send) - before; got != 4 {
		t.Errorf("unsubscribed client got %d of 4 events", got)
	}
}
