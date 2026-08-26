package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// ---------------------------------------------------------------------------
// Fakes implementing the daemon SessionManager dependency interfaces.
// ---------------------------------------------------------------------------

// fakeStore is an in-memory daemon.StoreInterface.
type fakeStore struct {
	mu        sync.Mutex
	sessions  map[string]store.Session
	order     []string // session ids in creation order
	msgs      map[string][]store.Message
	nextSess  int
	nextMsgID int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions: make(map[string]store.Session),
		msgs:     make(map[string][]store.Message),
	}
}

func (f *fakeStore) CreateSession(_ context.Context, metadata map[string]any) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSess++
	id := fmt.Sprintf("sess-%03d", f.nextSess)
	if metadata == nil {
		metadata = map[string]any{}
	}
	s := store.Session{ID: id, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(), Metadata: metadata}
	f.sessions[id] = s
	f.order = append(f.order, id)
	return s, nil
}

func (f *fakeStore) GetSession(_ context.Context, id string) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return store.Session{}, store.ErrSessionNotFound
	}
	return s, nil
}

func (f *fakeStore) UpdateSessionMetadata(_ context.Context, id string, metadata map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return store.ErrSessionNotFound
	}
	if s.Metadata == nil {
		s.Metadata = map[string]any{}
	}
	for k, v := range metadata {
		s.Metadata[k] = v
	}
	s.UpdatedAt = time.Now().UnixMilli()
	f.sessions[id] = s
	return nil
}

func (f *fakeStore) ListSessions(_ context.Context, limit, offset int) ([]store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Session
	for i, id := range f.order {
		if i < offset {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, f.sessions[id])
	}
	return out, nil
}

func (f *fakeStore) DeleteSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	delete(f.msgs, id)
	for i, v := range f.order {
		if v == id {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeStore) AppendMessage(_ context.Context, msg *store.Message) (int, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextMsgID++
	msg.ID = f.nextMsgID
	msg.Seq = len(f.msgs[msg.SessionID]) + 1
	msg.CreatedAt = time.Now().UnixMilli()
	f.msgs[msg.SessionID] = append(f.msgs[msg.SessionID], *msg)
	return msg.Seq, msg.ID, nil
}

func (f *fakeStore) GetMessages(_ context.Context, sessionID string, limit, offset int) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.msgs[sessionID]
	if offset > len(all) {
		return nil, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]store.Message, len(all))
	copy(out, all)
	return out, nil
}

func (f *fakeStore) GetMessagesSince(_ context.Context, sessionID string, sinceSeq int) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Message
	for _, m := range f.msgs[sessionID] {
		if m.Seq > sinceSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) Close() error { return nil }

// sessionCount returns how many sessions exist right now.
func (f *fakeStore) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// userMessageSession finds the newest session whose transcript contains a
// user message with the given content.
func (f *fakeStore) userMessageSession(content string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found string
	for _, id := range f.order {
		for _, m := range f.msgs[id] {
			if m.Role == "user" && m.Content == content {
				found = id
			}
		}
	}
	return found
}

// fakeProvider scripts LLM chat responses in order; the last response repeats
// once the script is exhausted.
type fakeProvider struct {
	mu     sync.Mutex
	models []string
	script []llm.ChatResponse
	chats  int
}

func newFakeProvider(models []string, script ...llm.ChatResponse) *fakeProvider {
	if len(models) == 0 {
		models = []string{"model-a"}
	}
	return &fakeProvider{models: models, script: script}
}

func plainReply(content string) llm.ChatResponse {
	return llm.ChatResponse{
		Model: "model-a",
		Choices: []llm.Choice{{
			Index:        0,
			Message:      llm.Message{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: &llm.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
}

func toolCallReply(content string, calls ...llm.ToolCall) llm.ChatResponse {
	return llm.ChatResponse{
		Model: "model-a",
		Choices: []llm.Choice{{
			Index:        0,
			Message:      llm.Message{Role: "assistant", Content: content, ToolCalls: calls},
			FinishReason: "tool_calls",
		}},
		Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
}

func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats++
	if len(f.script) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("no scripted responses left")
	}
	resp := f.script[0]
	if len(f.script) > 1 {
		f.script = f.script[1:]
	}
	return resp, nil
}

func (f *fakeProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("streaming not supported by fake provider")
}

func (f *fakeProvider) ListModels() ([]string, error) { return f.models, nil }
func (f *fakeProvider) Close() error                  { return nil }

func (f *fakeProvider) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chats
}

// fakeRegistry implements daemon.LLMRegistryInterface plus hot-swap.
type fakeRegistry struct {
	prov         *fakeProvider
	defaultModel string
}

func (r *fakeRegistry) GetDefault() (llm.Provider, string) { return r.prov, r.defaultModel }

func (r *fakeRegistry) SetDefault(model string) error {
	for _, m := range r.prov.models {
		if m == model {
			r.defaultModel = model
			return nil
		}
	}
	return fmt.Errorf("model %q not available in provider %q", model, "mock")
}

func (r *fakeRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return r.prov.Chat(ctx, req)
}

func (r *fakeRegistry) Close() error { return nil }

// fakeTools implements daemon.ToolsRegistryInterface.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool // tool names that always fail
}

func (t *fakeTools) List() []tools.Tool { return nil }

func (t *fakeTools) Execute(_ context.Context, name string, _ map[string]any) (tools.Result, error) {
	t.mu.Lock()
	t.calls = append(t.calls, name)
	fail := t.fail[name]
	t.mu.Unlock()
	if fail {
		return tools.Result{}, fmt.Errorf("tool %s exploded", name)
	}
	return tools.Result{Content: "tool-ok"}, nil
}

// fakePerms implements agent.PermsEngineInterface allowing everything.
type fakePerms struct{}

func (fakePerms) Check(perms.Request) perms.Decision {
	return perms.Decision{Allowed: true, Rule: "test-allow"}
}

// ---------------------------------------------------------------------------
// Stack builders.
// ---------------------------------------------------------------------------

// daemonStack bundles everything a client test may want to assert on.
type daemonStack struct {
	client    *Client
	store     *fakeStore
	provider  *fakeProvider
	registry  *fakeRegistry
	toolsReg  *fakeTools
	transport *daemon.Transport
	addr      string
}

// startTestDaemon spins up an in-memory forge daemon (real transport +
// handler + session manager over fakes) and connects a Client to it.
// Cleanups run client-close first, transport-stop second.
func startTestDaemon(t *testing.T, script ...llm.ChatResponse) *daemonStack {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	st := newFakeStore()
	prov := newFakeProvider([]string{"model-a", "model-b"}, script...)
	reg := &fakeRegistry{prov: prov, defaultModel: "model-a"}
	tl := &fakeTools{}
	emergency := daemon.NewEmergencyState(logger)

	mgr := daemon.NewSessionManager(st, reg, tl, emergency, logger, &config.Config{}, fakePerms{}, st)
	handler := daemon.NewHandler(mgr, logger)
	tx := daemon.NewTransport("127.0.0.1:0", handler, logger)
	if err := tx.Start(context.Background()); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { _ = tx.Stop() })

	cl, err := Connect(context.Background(), tx.Addr())
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	return &daemonStack{
		client:    cl,
		store:     st,
		provider:  prov,
		registry:  reg,
		toolsReg:  tl,
		transport: tx,
		addr:      tx.Addr(),
	}
}

// callCtx returns a bounded context for one test operation so nothing can
// hang the suite on Windows.
func callCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// awaitChan receives one value from ch or fails the test after d.
func awaitChan[T any](t *testing.T, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for channel value", d)
		var zero T
		return zero
	}
}

// ---------------------------------------------------------------------------
// Raw WebSocket server with drop control (for reconnect/failure tests).
// ---------------------------------------------------------------------------

// rawWSServer is a minimal JSON-RPC-ish WebSocket server whose connections
// the test can kill abruptly. It can also swallow requests (accept them but
// never answer).
type rawWSServer struct {
	srv      *httptest.Server
	addr     string
	mu       sync.Mutex
	conns    []*websocket.Conn
	swallow  atomic.Bool
	received chan struct{} // signaled per parsed inbound frame
	notifs   chan daemon.JSONRPCNotification
}

func newRawWSServer(t *testing.T) *rawWSServer {
	rw := &rawWSServer{
		received: make(chan struct{}, 64),
		notifs:   make(chan daemon.JSONRPCNotification, 64),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		rw.mu.Lock()
		rw.conns = append(rw.conns, conn)
		rw.mu.Unlock()
		defer func() {
			rw.mu.Lock()
			for i, c := range rw.conns {
				if c == conn {
					rw.conns = append(rw.conns[:i], rw.conns[i+1:]...)
					break
				}
			}
			rw.mu.Unlock()
		}()

		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, data, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}

			var probe struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
			}
			if json.Unmarshal(data, &probe) != nil {
				continue
			}

			switch {
			case probe.ID != nil:
				if rw.swallow.Load() {
					rw.received <- struct{}{}
					continue
				}
				resp := daemon.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      probe.ID,
					Result:  json.RawMessage(`{"echo":true}`),
				}
				frame, _ := json.Marshal(resp)
				wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = conn.Write(wctx, websocket.MessageText, frame)
				wcancel()
			case probe.Method != "":
				var notif daemon.JSONRPCNotification
				_ = json.Unmarshal(data, &notif)
				select {
				case rw.notifs <- notif:
				default:
				}
			}
			rw.received <- struct{}{}
		}
	})

	rw.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		rw.dropAll() // unblock handlers on hijacked conns before Close waits
		rw.srv.Close()
	})
	rw.addr = strings.TrimPrefix(rw.srv.URL, "http://")
	return rw
}

// dropAll force-closes every server-side connection without a close
// handshake, simulating an abrupt daemon-side death.
func (rw *rawWSServer) dropAll() {
	rw.mu.Lock()
	conns := append([]*websocket.Conn(nil), rw.conns...)
	rw.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
}
