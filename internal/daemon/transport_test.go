// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// Test transport with mock handler
type testHandler struct {
	*Handler
}

func newTestHandler(t *testing.T) *testHandler {
	logger := slog.New(slog.DiscardHandler)

	// Create a real SessionManager with mock dependencies
	store := &mockStoreForTransport{}
	llmReg := &mockLLMRegistryForTransport{}
	toolsReg := &mockToolsRegistryForTransport{}
	emergency := NewEmergencyState(logger)

	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, &config.Config{}, nil, nil)
	handler := NewHandler(mgr, logger)

	return &testHandler{Handler: handler}
}

// Mock store for transport tests
type mockStoreForTransport struct{}

func (m *mockStoreForTransport) CreateSession(ctx context.Context, metadata map[string]any) (store.Session, error) {
	return store.Session{ID: "test", CreatedAt: 1, UpdatedAt: 1, Metadata: metadata}, nil
}
func (m *mockStoreForTransport) GetSession(ctx context.Context, id string) (store.Session, error) {
	return store.Session{}, store.ErrSessionNotFound
}
func (m *mockStoreForTransport) UpdateSessionMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return nil
}
func (m *mockStoreForTransport) ListSessions(ctx context.Context, limit, offset int) ([]store.Session, error) {
	return nil, nil
}
func (m *mockStoreForTransport) DeleteSession(ctx context.Context, id string) error { return nil }
func (m *mockStoreForTransport) AppendMessage(ctx context.Context, msg *store.Message) (int, int64, error) {
	return 0, 0, nil
}
func (m *mockStoreForTransport) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	return nil, nil
}
func (m *mockStoreForTransport) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	return nil, nil
}
func (m *mockStoreForTransport) Close() error { return nil }

// Mock LLM registry for transport tests
type mockLLMRegistryForTransport struct{}

func (m *mockLLMRegistryForTransport) GetDefault() (llm.Provider, string) { return nil, "" }
func (m *mockLLMRegistryForTransport) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, nil
}
func (m *mockLLMRegistryForTransport) Close() error { return nil }

// Mock tools registry for transport tests
type mockToolsRegistryForTransport struct{}

func (m *mockToolsRegistryForTransport) List() []tools.Tool { return nil }
func (m *mockToolsRegistryForTransport) Execute(ctx context.Context, name string, args map[string]any) (tools.Result, error) {
	return tools.Result{}, nil
}

func TestTransportStartStop(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	addr := tx.Addr()
	if addr == "" {
		t.Error("expected non-empty address")
	}

	err = tx.Stop()
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
}

func TestTransportHealthEndpoint(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	req := httptest.NewRequest(http.MethodGet, "http://"+tx.Addr()+"/health", nil)
	w := httptest.NewRecorder()
	tx.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", w.Body.String())
	}
}

func TestTransportWebSocketConnect(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx := context.Background()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	url := "ws://" + tx.Addr() + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}

	// Send a ping to verify connection works
	ctxPing, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	err = conn.Ping(ctxPing)
	cancelPing()
	if err != nil {
		t.Errorf("ping failed: %v", err)
	}

	// Close the connection from client side
	conn.Close(websocket.StatusNormalClosure, "test done")

	// Give server time to process close
	time.Sleep(100 * time.Millisecond)

	// Now stop the transport
	err = tx.Stop()
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
}

func TestTransportJSONRPCRequest(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	url := "ws://" + tx.Addr() + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "test.method",
		Params:  json.RawMessage(`{}`),
	}
	reqData, _ := json.Marshal(req)
	conn.Write(ctx, websocket.MessageText, reqData)

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	if resp.Error == nil || resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected method not found error, got %+v", resp.Error)
	}
}

func TestTransportConcurrentClients(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	url := "ws://" + tx.Addr() + "/ws"
	const numClients = 10

	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("client %d dial failed: %v", i, err)
		}
		conns[i] = conn
		defer conn.Close(websocket.StatusNormalClosure, "test done")
	}

	for i, conn := range conns {
		req := JSONRPCRequest{JSONRPC: "2.0", Method: "ping"}
		reqData, _ := json.Marshal(req)
		if err := conn.Write(ctx, websocket.MessageText, reqData); err != nil {
			t.Errorf("client %d write failed: %v", i, err)
		}
		_, _, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("client %d read failed: %v", i, err)
		}
	}
}

func TestTransportHeartbeat(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	url := "ws://" + tx.Addr() + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	ctxPing, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	err = conn.Ping(ctxPing)
	cancelPing()
	if err != nil {
		t.Errorf("ping failed: %v", err)
	}
}

func TestTransportBroadcast(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := newTestHandler(t)
	tx := NewTransport("127.0.0.1:0", h.Handler, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tx.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer tx.Stop()

	url := "ws://" + tx.Addr() + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	notif, _ := NewNotification("test.broadcast", map[string]string{"msg": "hello"})
	tx.Broadcast("", notif)

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read broadcast failed: %v", err)
	}

	var received JSONRPCNotification
	if err := json.Unmarshal(data, &received); err != nil {
		t.Fatalf("unmarshal broadcast failed: %v", err)
	}

	if received.Method != "test.broadcast" {
		t.Errorf("expected method 'test.broadcast', got %s", received.Method)
	}
}
