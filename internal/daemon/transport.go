// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/eduardosanmartin/forge/internal/webui"
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

	// RF-7.4/RNF-4.11: remote-access auth + TLS. Empty/empty means both are
	// disabled, which is only permitted for a loopback bind — see Start.
	authTokenHash string
	sessions      *sessionStore
	tlsCertFile   string
	tlsKeyFile    string
}

// SetAuth configures the shared token (as SHA-256 hex, see HashToken) every
// non-loopback request must present. Call before Start. An empty hash
// disables auth, which Start only allows for a loopback bind.
func (t *Transport) SetAuth(tokenHash string) {
	t.authTokenHash = tokenHash
}

// SetTLS configures a PEM certificate+key pair Start serves over instead of
// plain HTTP. Call before Start. Empty paths mean plain HTTP, which Start
// only allows for a loopback bind.
func (t *Transport) SetTLS(certFile, keyFile string) {
	t.tlsCertFile = certFile
	t.tlsKeyFile = keyFile
}

// isLoopbackAddr reports whether addr's host resolves to loopback-only —
// the safety floor's dividing line between "needs nothing extra" (today's
// default) and "needs auth + TLS" (RF-7.4/RNF-4.11). An empty host (":8080")
// and wildcards (0.0.0.0, ::, ::0) mean "all interfaces", which is NOT
// loopback even though it's easy to mistake for a safe default.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // addr with no port (e.g. bare host) — best effort
	}
	if host == "" {
		return false // wildcard bind
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // unresolved hostname: do not assume it's safe
	}
	return ip.IsLoopback()
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
	// RF-7.4/RNF-4.11 safety floor: binding beyond loopback with no auth
	// and/or no TLS would expose the full JSON-RPC API (fs/shell/git tools
	// included) to the network in the clear. Refuse outright rather than
	// start insecurely — there is no --insecure-remote escape hatch by
	// design, matching this project's deny-by-default posture elsewhere
	// (internal/perms).
	if !isLoopbackAddr(t.addr) {
		var missing []string
		if t.authTokenHash == "" {
			missing = append(missing, "auth token (forge daemon set-password)")
		}
		if t.tlsCertFile == "" || t.tlsKeyFile == "" {
			missing = append(missing, "TLS certificate (--tls-cert/--tls-key or --tls-self-signed)")
		}
		if len(missing) > 0 {
			return fmt.Errorf("refusing to bind %q (not loopback) without: %s", t.addr, strings.Join(missing, ", "))
		}
	}

	rawListener, err := net.Listen("tcp", t.addr)
	if err != nil {
		return err
	}
	t.listener = rawListener

	if t.tlsCertFile != "" && t.tlsKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.tlsCertFile, t.tlsKeyFile)
		if err != nil {
			_ = rawListener.Close()
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		t.listener = tls.NewListener(rawListener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
	}

	t.addr = rawListener.Addr().String()
	t.sessions = newSessionStore()

	mux := http.NewServeMux()
	mux.Handle("/ws", t.requireAuth(http.HandlerFunc(t.handleWebSocket)))
	mux.HandleFunc("/health", t.handleHealth)
	mux.HandleFunc("/auth/status", t.handleAuthStatus)
	mux.HandleFunc("/auth/login", t.handleAuthLogin)
	mux.HandleFunc("/auth/logout", t.handleAuthLogout)
	// RF-7.2: serve the embedded session GUI on every other path. It is
	// static assets only, deliberately left unauthenticated — the GUI shell
	// itself carries nothing sensitive, and it is what renders the login
	// form in the first place. Every route that actually touches session
	// data (/ws) is gated above. A handler failure here must not stop the
	// daemon from serving /ws and /health, so it degrades to a 404 instead
	// of an error.
	if guiHandler, err := webui.Handler(); err == nil {
		mux.Handle("/", guiHandler)
	} else if t.logger != nil {
		t.logger.Warn("webui: embedded assets unavailable, GUI route disabled", "error", err)
	}

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

// wsReadLimitBytes overrides coder/websocket's 32 KiB default per-message
// read limit — mirrors internal/client's own wsReadLimitBytes (kept as a
// separate constant here since internal/daemon cannot import internal/client).
// A session.execute_turn response carries the full turn transcript as one
// JSON-RPC message and routinely exceeds 32 KiB for anything beyond a
// trivial exchange; the default previously closed the connection outright
// with StatusMessageTooBig (observed in practice via the RF-11 manifest
// decomposition feature against a real model).
const wsReadLimitBytes = 16 * 1024 * 1024

// handleWebSocket upgrades HTTP to WebSocket and starts the client handler.
func (t *Transport) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		if t.logger != nil {
			t.logger.Error("websocket accept failed", "error", err)
		}
		return
	}
	conn.SetReadLimit(wsReadLimitBytes)

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
