// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Transport handles WebSocket connections and JSON-RPC message dispatch.
type Transport struct {
	addr      string
	handler   *Handler
	logger    *slog.Logger
	server    *http.Server
	listener  net.Listener
	conns     map[*websocket.Conn]*ClientConn
	connsMu   sync.RWMutex
	broadcast chan *JSONRPCNotification
	stopping  atomic.Bool
	wg        sync.WaitGroup
}

// ClientConn represents a connected client with its session subscriptions.
type ClientConn struct {
	conn          *websocket.Conn
	subscriptions map[string]bool // session IDs this client is subscribed to
	send          chan []byte
	done          chan struct{}
	doneOnce      sync.Once // guards close(done): both Stop() and readLoop exit paths close it
	readCtx       context.Context
	readCancel    context.CancelFunc
}

// closeDone closes cc.done exactly once, regardless of caller.
func (cc *ClientConn) closeDone() {
	cc.doneOnce.Do(func() { close(cc.done) })
}

// NewTransport creates a new Transport.
func NewTransport(addr string, handler *Handler, logger *slog.Logger) *Transport {
	return &Transport{
		addr:      addr,
		handler:   handler,
		logger:    logger,
		conns:     make(map[*websocket.Conn]*ClientConn),
		broadcast: make(chan *JSONRPCNotification, 256),
	}
}

// Start starts the WebSocket server.
func (t *Transport) Start(ctx context.Context) error {
	var err error
	t.listener, err = net.Listen("tcp", t.addr)
	if err != nil {
		return err
	}

	t.addr = t.listener.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", t.handleWebSocket)
	mux.HandleFunc("/health", t.handleHealth)

	t.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if err := t.server.Serve(t.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if t.logger != nil {
				t.logger.Error("http server error", "error", err)
			}
		}
	}()

	t.wg.Add(1)
	go t.broadcastLoop(ctx)

	if t.logger != nil {
		t.logger.Info("transport started", "addr", t.addr)
	}
	return nil
}

// Addr returns the listening address.
func (t *Transport) Addr() string {
	return t.addr
}

// Stop stops the transport gracefully.
func (t *Transport) Stop() error {
	t.stopping.Store(true)
	close(t.broadcast)

	// Cancel all read contexts to unblock readLoop
	t.connsMu.Lock()
	for _, cc := range t.conns {
		if cc.readCancel != nil {
			cc.readCancel()
		}
		_ = cc.conn.Close(websocket.StatusNormalClosure, "server shutting down")
		// Safely close channels - they might already be closed by readLoop
		func() {
			defer func() { recover() }()
			close(cc.send)
		}()
		cc.closeDone()
	}
	t.connsMu.Unlock()

	if t.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := t.server.Shutdown(ctx); err != nil {
			return err
		}
	}

	t.wg.Wait()
	if t.logger != nil {
		t.logger.Info("transport stopped")
	}
	return nil
}

// handleWebSocket upgrades HTTP to WebSocket and starts the client handler.
func (t *Transport) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		if t.logger != nil {
			t.logger.Error("websocket accept failed", "error", err)
		}
		return
	}

	readCtx, readCancel := context.WithCancel(context.Background())
	cc := &ClientConn{
		conn:          conn,
		subscriptions: make(map[string]bool),
		send:          make(chan []byte, 64),
		done:          make(chan struct{}),
		readCtx:       readCtx,
		readCancel:    readCancel,
	}

	t.connsMu.Lock()
	t.conns[conn] = cc
	t.connsMu.Unlock()

	t.wg.Add(1)
	go cc.readLoop(t)
	t.wg.Add(1)
	go cc.writeLoop(t)
}

// handleHealth returns a simple health check endpoint.
func (t *Transport) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// broadcastLoop sends notifications to subscribed clients.
func (t *Transport) broadcastLoop(ctx context.Context) {
	defer t.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case notif, ok := <-t.broadcast:
			if !ok {
				return
			}
			t.dispatchNotification(notif)
		}
	}
}

// Broadcast sends a notification to all clients subscribed to the session (or all if sessionID is empty).
func (t *Transport) Broadcast(sessionID string, notif *JSONRPCNotification) {
	select {
	case t.broadcast <- notif:
	default:
		if t.logger != nil {
			t.logger.Warn("broadcast channel full, dropping notification", "method", notif.Method)
		}
	}
}

// dispatchNotification delivers a notification to matching clients.
func (t *Transport) dispatchNotification(notif *JSONRPCNotification) {
	data, err := json.Marshal(notif)
	if err != nil {
		if t.logger != nil {
			t.logger.Error("marshal notification failed", "error", err)
		}
		return
	}

	t.connsMu.RLock()
	defer t.connsMu.RUnlock()

	for _, cc := range t.conns {
		// Global notifications (empty sessionID) go to all clients
		// Session-specific notifications go only to subscribed clients
		if notif.Method == MethodEmergencyHalt || len(cc.subscriptions) == 0 || cc.subscriptions[notif.Method] {
			// For session-specific events, check subscription
			if notif.Method != MethodEmergencyHalt && len(cc.subscriptions) > 0 {
				// Extract sessionID from notification params if possible
				// For simplicity, broadcast to all subscribed clients for session events
				select {
				case cc.send <- data:
				default:
					// Client send buffer full, skip
				}
			} else {
				select {
				case cc.send <- data:
				default:
				}
			}
		}
	}
}

// Subscribe adds a session subscription for a client.
func (t *Transport) Subscribe(conn *websocket.Conn, sessionID string) {
	t.connsMu.Lock()
	defer t.connsMu.Unlock()
	if cc, ok := t.conns[conn]; ok {
		cc.subscriptions[sessionID] = true
	}
}

// Unsubscribe removes a session subscription for a client.
func (t *Transport) Unsubscribe(conn *websocket.Conn, sessionID string) {
	t.connsMu.Lock()
	defer t.connsMu.Unlock()
	if cc, ok := t.conns[conn]; ok {
		delete(cc.subscriptions, sessionID)
	}
}

// removeClient removes a client from the connection map.
func (t *Transport) removeClient(conn *websocket.Conn) {
	t.connsMu.Lock()
	defer t.connsMu.Unlock()
	if _, ok := t.conns[conn]; ok {
		delete(t.conns, conn)
	}
}

// readLoop reads messages from the WebSocket connection.
func (cc *ClientConn) readLoop(t *Transport) {
	defer t.wg.Done()
	defer t.removeClient(cc.conn)
	defer cc.closeDone()

	for {
		_, data, err := cc.conn.Read(cc.readCtx)
		if err != nil {
			if t.logger != nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.logger.Debug("websocket read error", "error", err)
			}
			return
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(data, &req); err != nil {
			if t.logger != nil {
				t.logger.Warn("invalid json-rpc request", "error", err)
			}
			continue
		}

		// Handle request bound to the CONNECTION lifetime: when the client
		// goes away (drop, close, or transport stop), any in-flight work —
		// above all a long agent turn — is cancelled instead of continuing
		// as an invisible zombie holding the LLM provider busy.
		resp := t.handler.HandleRequest(cc.readCtx, &req)
		if resp != nil {
			respData, _ := json.Marshal(resp)
			select {
			case cc.send <- respData:
			default:
			}
		}
	}
}

// writeLoop writes messages to the WebSocket connection.
func (cc *ClientConn) writeLoop(t *Transport) {
	defer t.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data, ok := <-cc.send:
			if !ok {
				_ = cc.conn.Close(websocket.StatusNormalClosure, "server shutting down")
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := cc.conn.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			// Best-effort keepalive ONLY. coder/websocket's Ping blocks
			// until the peer's pong is processed by a concurrent Read on
			// OUR side of this connection; while a long agent turn occupies
			// readLoop that pong cannot be processed, so a ping timeout does
			// NOT imply the peer disappeared. Killing the write loop here
			// would strand every future response in cc.send behind a dead
			// writer (observed as clients idling out mid-turn against slow
			// local models). Real connection deaths surface as Write errors.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = cc.conn.Ping(ctx)
			cancel()
		case <-cc.done:
			return
		}
	}
}
