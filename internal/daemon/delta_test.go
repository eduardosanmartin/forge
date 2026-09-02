package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
)

// streamingBridgeProvider emits text deltas via ChatStream.
type streamingBridgeProvider struct {
	content string
	resp    llm.ChatResponse
}

func (p *streamingBridgeProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return p.resp, nil
}
func (p *streamingBridgeProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 16)
	go func() {
		defer close(ch)
		for _, c := range p.content {
			ch <- llm.StreamChunk{
				ID:    "bridge",
				Model: req.Model,
				Choices: []llm.StreamChoice{{Delta: llm.Message{Role: "assistant", Content: string(c)}}},
			}
		}
		fr := "stop"
		ch <- llm.StreamChunk{
			ID:    "bridge",
			Model: req.Model,
			Choices: []llm.StreamChoice{{FinishReason: &fr, Delta: llm.Message{Role: "assistant"}}},
		}
	}()
	return ch, nil
}
func (p *streamingBridgeProvider) ListModels() ([]string, error) { return []string{"test-model"}, nil }
func (p *streamingBridgeProvider) Close() error                  { return nil }

type bridgeTestRegistry struct {
	prov llm.Provider
}

func (r *bridgeTestRegistry) GetDefault() (llm.Provider, string) { return r.prov, "test-model" }
func (r *bridgeTestRegistry) GetProvider(name string) (llm.Provider, bool) {
	return r.prov, true
}
func (r *bridgeTestRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.prov.Chat(ctx, req)
}
func (r *bridgeTestRegistry) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return r.prov.ChatStream(ctx, req)
}
func (r *bridgeTestRegistry) Close() error                  { return r.prov.Close() }
func (r *bridgeTestRegistry) ListAll() []llm.ModelInfo      { return nil }
func (r *bridgeTestRegistry) SetDefault(model string) error { return nil }

func TestSessionManager_DeltaBridge_PublishesDeltas(t *testing.T) {
	// Verify that when streaming is enabled and a publisher is wired,
	// the SessionManager publishes message.delta.event notifications per text delta.
	cfg := config.Defaults()
	cfg.LLM.Streaming = true

	store := newTestStore()
	prov := &streamingBridgeProvider{
		content: "hello",
		resp: llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}},
		},
	}
	reg := &bridgeTestRegistry{prov: prov}

	toolsReg := newTestToolsRegistry()
	emergency := NewEmergencyState(nil)
	permsEng := newTestPermsEngine()
	mgr := NewSessionManager(store, reg, toolsReg, emergency, nil, cfg, permsEng, store)

	var mu sync.Mutex
	var deltas []string
	var notified []string
	publisher := func(sessionID string, notif *JSONRPCNotification) {
		if notif.Method != MethodMessageDelta {
			t.Errorf("unexpected method %q", notif.Method)
			return
		}
		var payload MessageDeltaPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			t.Errorf("unmarshal delta payload: %v", err)
			return
		}
		mu.Lock()
		deltas = append(deltas, payload.Delta)
		notified = append(notified, payload.SessionID)
		mu.Unlock()
	}
	mgr.SetDeltaPublisher(publisher)

	session, err := mgr.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Execute turn; the manager should invoke streaming path and call publisher per delta.
	_, err = mgr.ExecuteTurn(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	// Allow publisher to flush (synchronous, but guard).
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	joined := ""
	for _, d := range deltas {
		joined += d
	}
	if joined != "hello" {
		t.Fatalf("deltas joined %q want hello, got %v", joined, deltas)
	}
	if len(notified) != 5 {
		t.Fatalf("expected 5 delta notifications, got %d", len(notified))
	}
	for _, sid := range notified {
		if sid != session.ID {
			t.Errorf("delta session_id %q want %q", sid, session.ID)
		}
	}
	// Verify that the final stored message content matches assembled deltas (parity).
	msgs, _ := mgr.GetMessagesSince(context.Background(), session.ID, 0)
	if len(msgs) == 0 {
		t.Fatal("no messages stored")
	}
	// Last assistant message should be "hello"
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stored messages do not contain assembled hello: %+v", msgs)
	}
}


