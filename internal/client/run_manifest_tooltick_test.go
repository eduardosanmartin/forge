package client

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/run"
)

// TestManifestExecutor_OnToolTick_ReceivesLiveEvents is an end-to-end
// regression lock for Incremento B: a real (in-memory) daemon executes a
// task turn that calls one tool, and ManifestExecutor's onToolTick callback
// must observe started+finished for that exact tool call while the
// blocking cl.Call is still in flight — not after the fact, not for a
// different session. Unlike startTestDaemon (harness_test.go), this stack
// wires SetDeltaPublisher, since the shared harness intentionally doesn't
// and tool.call.event is silently dropped without it.
func TestManifestExecutor_OnToolTick_ReceivesLiveEvents(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st := newFakeStore()
	prov := newFakeProvider([]string{"model-a"},
		toolCallReply("", llm.ToolCall{ID: "call-1", Type: "function", Function: llm.ToolCallFunction{Name: "fs_list", Arguments: "{}"}}),
		plainReply("done"),
	)
	reg := &fakeRegistry{prov: prov, defaultModel: "model-a"}
	tl := &fakeTools{}
	emergency := daemon.NewEmergencyState(logger)

	mgr := daemon.NewSessionManager(st, reg, tl, emergency, logger, &config.Config{}, fakePerms{}, st)
	handler := daemon.NewHandler(mgr, logger, nil, nil)
	tx := daemon.NewTransport("127.0.0.1:0", handler, logger)
	// The one thing startTestDaemon's shared stack does NOT wire — without
	// it, tool.call.event and message.delta are constructed but never sent
	// (session_mgr.go's onToolEvent no-ops when m.deltaPublisher is nil).
	mgr.SetDeltaPublisher(func(sessionID string, notif *daemon.JSONRPCNotification) {
		tx.Broadcast(sessionID, notif)
	})
	if err := tx.Start(context.Background()); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { _ = tx.Stop() })

	cl, err := Connect(context.Background(), tx.Addr())
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	ctx, cancel := callCtx(t)
	defer cancel()
	var created daemon.SessionResult
	if err := cl.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{}, &created); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := created.ID

	var mu sync.Mutex
	var ticks []daemon.ToolCallEventPayload
	exec := ManifestExecutor(ctx, cl, sessionID, func(ev daemon.ToolCallEventPayload) {
		mu.Lock()
		ticks = append(ticks, ev)
		mu.Unlock()
	})

	if _, err := exec(ctx, run.Task{ID: "t1", Goal: "list files"}); err != nil {
		t.Fatalf("exec task: %v", err)
	}

	// The tick(s) arrive over the WebSocket asynchronously relative to the
	// blocking Call's own return — give delivery a moment rather than
	// asserting the instant exec() returns.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(ticks)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ticks) != 2 {
		t.Fatalf("got %d ticks, want 2 (started+finished): %+v", len(ticks), ticks)
	}
	if ticks[0].Status != "started" || ticks[0].Name != "fs_list" || ticks[0].SessionID != sessionID {
		t.Errorf("tick[0] = %+v, want started/fs_list/%s", ticks[0], sessionID)
	}
	if ticks[1].Status != "finished" || ticks[1].Name != "fs_list" {
		t.Errorf("tick[1] = %+v, want finished/fs_list", ticks[1])
	}
}
