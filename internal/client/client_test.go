package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestResolveDaemonAddrExplicit(t *testing.T) {
	got, err := ResolveDaemonAddr("  127.0.0.1:7777  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "127.0.0.1:7777" {
		t.Errorf("explicit addr should be trimmed, got %q", got)
	}
}

func TestResolveDaemonAddrFromFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	if _, err := ResolveDaemonAddr(""); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("missing file: want ErrDaemonNotRunning, got %v", err)
	}

	forgeDir := filepath.Join(tmp, ".forge")
	if err := os.MkdirAll(forgeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	addrFile := filepath.Join(forgeDir, "daemon.addr")
	if err := os.WriteFile(addrFile, []byte("127.0.0.1:4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveDaemonAddr("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "127.0.0.1:4242" {
		t.Errorf("want 127.0.0.1:4242, got %q", got)
	}

	if err := os.WriteFile(addrFile, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDaemonAddr(""); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("empty file: want ErrDaemonNotRunning, got %v", err)
	}
}

func TestConnectToMissingDaemonFailsFast(t *testing.T) {
	start := time.Now()
	_, err := Connect(context.Background(), "127.0.0.1:1") // port 1: refused
	if !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("want ErrDaemonNotRunning, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("connect should fail fast, took %s", elapsed)
	}
}

func TestCallRoundTripEcho(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	ctx, cancel := callCtx(t)
	defer cancel()
	var res struct {
		Echo bool `json:"echo"`
	}
	if err := cl.Call(ctx, "anything.method", map[string]string{"x": "y"}, &res); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if !res.Echo {
		t.Error("expected echo result true")
	}
}

func TestCallUnknownMethodIsTypedRPCError(t *testing.T) {
	stack := startTestDaemon(t)

	ctx, cancel := callCtx(t)
	defer cancel()
	err := stack.client.Call(ctx, "does.not.exist", nil, nil)
	if !IsCode(err, daemon.ErrCodeMethodNotFound) {
		t.Fatalf("want method-not-found code %d, got %v", daemon.ErrCodeMethodNotFound, err)
	}
}

func TestCallSessionNotFoundIsTypedRPCError(t *testing.T) {
	stack := startTestDaemon(t)

	ctx, cancel := callCtx(t)
	defer cancel()
	err := stack.client.Call(ctx, daemon.MethodGetSession,
		daemon.GetSessionParams{SessionID: "nope"}, nil)
	if !IsCode(err, daemon.ErrCodeSessionNotFound) {
		t.Fatalf("want session-not-found code %d, got %v", daemon.ErrCodeSessionNotFound, err)
	}
}

func TestNotifyReachesServer(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	ctx, cancel := callCtx(t)
	defer cancel()
	if err := cl.Notify(ctx, "test.note", map[string]int{"n": 1}); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	select {
	case n := <-rw.notifs:
		if n.Method != "test.note" {
			t.Errorf("got notification for %q", n.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the notification")
	}
}

func TestEventsStreamFromDaemonBroadcast(t *testing.T) {
	stack := startTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := stack.client.Events(ctx)
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}

	notif, _ := daemon.NewNotification("test.broadcast", map[string]string{"k": "v"})
	stack.transport.Broadcast("", notif)

	// Generous under load: full-suite parallel runs on modest hardware
	// have observed >5s scheduling delays before the broadcast lands.
	got := awaitChan(t, events, 12*time.Second)
	if got.Method != "test.broadcast" {
		t.Errorf("want test.broadcast, got %q", got.Method)
	}
}

func TestEventsMultipleSubscribersAllReceive(t *testing.T) {
	stack := startTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := stack.client.Events(ctx)
	if err != nil {
		t.Fatalf("subscribe a: %v", err)
	}
	b, err := stack.client.Events(ctx)
	if err != nil {
		t.Fatalf("subscribe b: %v", err)
	}

	notif, _ := daemon.NewNotification("test.fanout", map[string]bool{})
	stack.transport.Broadcast("", notif)

	for i, ch := range []<-chan daemon.JSONRPCNotification{a, b} {
		got := awaitChan(t, ch, 5*time.Second)
		if got.Method != "test.fanout" {
			t.Errorf("subscriber %d: want test.fanout, got %q", i, got.Method)
		}
	}
}

func TestConcurrentCallsAllSucceed(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var res struct {
				Echo bool `json:"echo"`
			}
			if err := cl.Call(ctx, "ping", nil, &res); err != nil {
				errs <- err
				return
			}
			if !res.Echo {
				errs <- errors.New("echo false")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}
}

func TestCallWithCanceledContextFailsPromptly(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled
	if err := cl.Call(ctx, "ping", nil, nil); err == nil {
		t.Fatal("expected canceled-context error")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	done := make(chan error, 2)
	go func() { done <- cl.Close() }()
	go func() { done <- cl.Close() }()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Close did not return promptly (possible hang)")
		}
	}
}

func TestInFlightCallFailsWhenConnectionDrops(t *testing.T) {
	rw := newRawWSServer(t)
	rw.swallow.Store(true)
	cl := connectRaw(t, rw.addr)

	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		callDone <- cl.Call(ctx, "slow.turn", nil, nil)
	}()

	// Wait until the request reached the server, then kill the socket.
	awaitChan(t, rw.received, 5*time.Second)
	rw.dropAll()

	select {
	case err := <-callDone:
		if !IsCode(err, -32000) || !strings.Contains(err.Error(), errConnectionLost.Error()) {
			t.Fatalf("in-flight call should fail with connection-lost error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight call hung after connection drop")
	}
}

func TestClientReconnectsAfterAbruptDrop(t *testing.T) {
	rw := newRawWSServer(t)
	cl := connectRaw(t, rw.addr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res struct {
		Echo bool `json:"echo"`
	}
	if err := cl.Call(ctx, "before.drop", nil, &res); err != nil {
		t.Fatalf("pre-drop call failed: %v", err)
	}

	// Abrupt server-side death without close frame.
	rw.dropAll()

	// The client must transparently reconnect and serve new calls.
	deadline := time.Now().Add(10 * time.Second)
	for {
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := cl.Call(cctx, "after.reconnect", nil, &res)
		ccancel()
		if err == nil && res.Echo {
			break // reconnected
		}
		if time.Now().After(deadline) {
			t.Fatalf("client never reconnected: last error %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// connectRaw connects a Client to a rawWSServer address (bypassing the
// daemon.addr file).
func connectRaw(t *testing.T, addr string) *Client {
	t.Helper()
	cl, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}
