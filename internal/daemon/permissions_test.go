package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestPermissionBrokerRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var events []*JSONRPCNotification
	b := newPermissionBroker(func(n *JSONRPCNotification) { mu.Lock(); events = append(events, n); mu.Unlock() }, func() bool { return true })

	done := make(chan tools.AskDecision, 1)
	go func() {
		done <- b.Ask(context.Background(), tools.AskRequest{Tool: "shell_exec", Rule: "ask:shell.exec:npm", Summary: "npm install"})
	}()

	var id string
	deadline := time.Now().Add(2 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		if p := b.Pending(); len(p) == 1 {
			id = p[0].RequestID
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("request never became pending")
	}
	if err := b.Respond(id, "bogus"); err == nil {
		t.Fatal("invalid decision must be rejected")
	}
	if err := b.Respond(id, "allow_session"); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != tools.AskAllowSession {
		t.Fatalf("decision = %s", got)
	}
	if err := b.Respond(id, "deny"); err == nil {
		t.Fatal("a second answer must be rejected (first answer wins)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Method != MethodPermissionRequestEvent || events[1].Method != MethodPermissionResolvedEvent {
		t.Fatalf("events = %v", events)
	}
	var payload PermissionRequestPayload
	_ = json.Unmarshal(events[0].Params, &payload)
	if payload.Summary != "npm install" || payload.RequestID != id {
		t.Fatalf("request payload = %+v", payload)
	}
}

func TestPermissionBrokerDeniesWithoutClientsOrOnTimeout(t *testing.T) {
	b := newPermissionBroker(nil, func() bool { return false })
	if got := b.Ask(context.Background(), tools.AskRequest{}); got != tools.AskDeny {
		t.Fatalf("no clients: %s, want deny", got)
	}
	b = newPermissionBroker(nil, func() bool { return true })
	b.timeout = 20 * time.Millisecond
	if got := b.Ask(context.Background(), tools.AskRequest{}); got != tools.AskDeny {
		t.Fatalf("timeout: %s, want deny", got)
	}
	if len(b.Pending()) != 0 {
		t.Fatal("expired request must be cleaned up")
	}
}
