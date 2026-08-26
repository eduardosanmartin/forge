// Package client implements forge's daemon clients: a reconnecting
// JSON-RPC-over-WebSocket transport, an interactive REPL session runner, and
// a non-interactive one-shot mode with structured JSON output.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

const (
	// dialTimeout bounds every individual WebSocket dial attempt.
	dialTimeout = 3 * time.Second

	// callWriteTimeout bounds writing a single request frame.
	callWriteTimeout = 10 * time.Second

	// readIdleTimeout bounds each blocking frame read as a wedge-protection
	// backstop: it must comfortably exceed the daemon's 30s ping interval so
	// healthy connections survive (library-consumed pings do NOT return from
	// Read, so a quiet-but-alive connection legitimately sees no data frames),
	// while still unblocking reads against a peer that died without a close
	// frame (Windows: coder/websocket Read may hang indefinitely on abrupt
	// close without a deadline). It must also stay ABOVE the provider-side
	// LLM call cap (internal/llm: 15 minutes): a long local-model turn is
	// legitimately silent on the wire until its response frame finally
	// arrives.
	readIdleTimeout = 20 * time.Minute

	// closeGracePeriod bounds the graceful close handshake watchdog.
	closeGracePeriod = 5 * time.Second

	// Reconnect backoff schedule: start here, double up to reconnectMax,
	// give up once reconnectBudget of cumulative waiting has elapsed.
	reconnectBase   = 100 * time.Millisecond
	reconnectMax    = 2 * time.Second
	reconnectBudget = 30 * time.Second

	// eventsBuffer is the per-subscriber notification channel capacity.
	// Notifications that overflow are dropped (best-effort stream).
	eventsBuffer = 64
)

// ErrDaemonNotRunning reports that no forge daemon could be reached, either
// because ~/.forge/daemon.addr is missing or the endpoint is unreachable.
var ErrDaemonNotRunning = errors.New("forge daemon is not running")

// ErrClientClosed reports that the client was shut down before or during an
// operation.
var ErrClientClosed = errors.New("client closed")

// errConnectionLost fails calls that were in flight when the underlying
// connection dropped. The client transparently reconnects, but in-flight
// requests are not retried automatically (turns are not idempotent).
var errConnectionLost = errors.New("connection to daemon lost")

// ResolveDaemonAddr returns the daemon address to connect to: explicit when
// non-empty, otherwise the contents of ~/.forge/daemon.addr written by
// `forge serve`.
func ResolveDaemonAddr(explicit string) (string, error) {
	if explicit != "" {
		return strings.TrimSpace(explicit), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	path := filepath.Join(home, ".forge", "daemon.addr")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w (missing %s)", ErrDaemonNotRunning, path)
	}
	addr := strings.TrimSpace(string(data))
	if addr == "" {
		return "", fmt.Errorf("%w (%s is empty)", ErrDaemonNotRunning, path)
	}
	return addr, nil
}

// Client is a JSON-RPC 2.0 client over WebSocket connected to the forge
// daemon. It is safe for concurrent use.
//
// If the connection drops while the client is open, Call blocks until the
// automatic reconnection succeeds (exponential backoff 100ms..2s, ~30s total
// budget), then proceeds over the fresh connection. In-flight requests are
// failed with errConnectionLost rather than replayed.
type Client struct {
	url      string
	addr     string
	logger   *slog.Logger
	lifeCtx  context.Context
	lifeStop context.CancelFunc

	writeMu sync.Mutex

	connMu   sync.Mutex
	conn     *websocket.Conn
	connWait chan struct{} // closed whenever a new connection becomes active

	pendingMu sync.Mutex
	pending   map[string]chan *daemon.JSONRPCResponse

	subsMu sync.Mutex
	subs   map[chan daemon.JSONRPCNotification]struct{}

	nextID atomic.Int64

	shutdownOnce sync.Once
	done         chan struct{} // closed when shutdown begins
	runDone      chan struct{} // closed when the serve loop exits
	termErr      error
}

// Connect dials the forge daemon and returns a ready Client. When addr is
// empty it is resolved from ~/.forge/daemon.addr.
//
// The initial dial is performed once with a short timeout so callers get an
// immediate actionable error (see ErrDaemonNotRunning) instead of a retry
// storm against a daemon that was never started. Automatic reconnection with
// backoff only applies to connections established successfully first.
func Connect(ctx context.Context, addr string) (*Client, error) {
	resolved, err := ResolveDaemonAddr(addr)
	if err != nil {
		return nil, err
	}

	logger := slog.New(slog.DiscardHandler)

	lifeCtx, lifeStop := context.WithCancel(context.Background())
	c := &Client{
		addr:     resolved,
		url:      "ws://" + resolved + "/ws",
		logger:   logger,
		lifeCtx:  lifeCtx,
		lifeStop: lifeStop,
		connWait: make(chan struct{}),
		pending:  make(map[string]chan *daemon.JSONRPCResponse),
		subs:     make(map[chan daemon.JSONRPCNotification]struct{}),
		done:     make(chan struct{}),
		runDone:  make(chan struct{}),
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, c.url, nil)
	if err != nil {
		lifeStop()
		return nil, fmt.Errorf("%w: dial %s: %v", ErrDaemonNotRunning, c.url, err)
	}

	c.setConn(conn)
	go c.serve(conn)
	return c, nil
}

// Addr returns the daemon address this client connects to.
func (c *Client) Addr() string { return c.addr }

// setConn publishes a newly established connection and wakes waiters.
func (c *Client) setConn(conn *websocket.Conn) {
	c.connMu.Lock()
	c.conn = conn
	close(c.connWait) // wake everything waiting for a connection
	c.connWait = make(chan struct{})
	c.connMu.Unlock()
}

// clearConn unpublishes a connection that just died. Waiters created after
// this point block until the next successful reconnect.
func (c *Client) clearConn(target *websocket.Conn) {
	c.connMu.Lock()
	if c.conn == target {
		c.conn = nil
		// connWait was already closed when target was published; replace it
		// so subsequent waiters block until the next setConn.
		select {
		case <-c.connWait:
			c.connWait = make(chan struct{})
		default:
			// A newer connection was already published; leave as-is.
		}
	}
	c.connMu.Unlock()
}

// activeConn waits for a live connection, the context to end, or the client
// to shut down.
func (c *Client) activeConn(ctx context.Context) (*websocket.Conn, error) {
	for {
		c.connMu.Lock()
		conn := c.conn
		wait := c.connWait
		c.connMu.Unlock()

		if conn != nil {
			return conn, nil
		}
		select {
		case <-wait:
			continue
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for daemon connection: %w", ctx.Err())
		case <-c.done:
			return nil, c.terminalError()
		}
	}
}

// serve owns the connection lifecycle: it pumps frames from the current
// connection and reconnects with exponential backoff after drops.
func (c *Client) serve(first *websocket.Conn) {
	defer close(c.runDone)

	conn := first
	for {
		c.readPump(conn)

		if c.isShuttingDown() {
			return
		}

		// Connection dropped: stop routing work to it immediately.
		c.clearConn(conn)
		c.failPending(errConnectionLost)

		// Bounded close handshake on the dead socket (no-op if already gone).
		closeWithDeadline(conn, c.logger)

		next := c.reconnect()
		if next == nil {
			return
		}
		conn = next
	}
}

// reconnect dials repeatedly with exponential backoff (100ms doubling to a
// 2s cap) until the client shuts down or ~30s of cumulative waiting elapses,
// whichever comes first. A nil return ends the serve loop.
func (c *Client) reconnect() *websocket.Conn {
	backoff := reconnectBase
	waited := time.Duration(0)
	for {
		select {
		case <-c.done:
			return nil
		case <-time.After(backoff):
		}
		waited += backoff
		backoff = min(backoff*2, reconnectMax)

		dialCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		conn, _, err := websocket.Dial(dialCtx, c.url, nil)
		cancel()
		if err == nil {
			c.setConn(conn)
			c.logger.Info("reconnected to daemon", "addr", c.addr)
			return conn
		}
		c.logger.Debug("daemon reconnect failed", "error", err, "waited", waited.String())

		if waited >= reconnectBudget {
			err := fmt.Errorf("daemon unreachable for %s: last error: %w", reconnectBudget, err)
			c.logger.Error("giving up reconnection", "error", err)
			c.shutdown(err)
			return nil
		}
	}
}

// readPump reads frames from conn until it errors out or the client shuts
// down. Every read carries a deadline so a peer that dies without a close
// frame cannot wedge the client forever.
func (c *Client) readPump(conn *websocket.Conn) {
	for {
		readCtx, cancel := context.WithTimeout(c.lifeCtx, readIdleTimeout)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			if !c.isShuttingDown() && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				c.logger.Debug("daemon read ended", "error", err)
			}
			return
		}
		c.handleFrame(data)
	}
}

// handleFrame routes one incoming frame to its pending caller or to event
// subscribers.
func (c *Client) handleFrame(data []byte) {
	var probe struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		c.logger.Warn("discarding malformed daemon frame", "error", err)
		return
	}

	switch {
	case probe.ID != nil:
		var resp daemon.JSONRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			c.logger.Warn("discarding malformed daemon response", "error", err)
			return
		}
		key := string(*probe.ID)
		c.pendingMu.Lock()
		ch, ok := c.pending[key]
		delete(c.pending, key)
		c.pendingMu.Unlock()
		if ok {
			ch <- &resp
		}

	case probe.Method != "":
		var notif daemon.JSONRPCNotification
		if err := json.Unmarshal(data, &notif); err != nil {
			c.logger.Warn("discarding malformed daemon notification", "error", err)
			return
		}
		c.dispatch(notif)
	}
}

// dispatch fans a notification out to all subscribers. Slow subscribers drop
// notifications rather than stalling the read pump.
func (c *Client) dispatch(notif daemon.JSONRPCNotification) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- notif:
		default:
			c.logger.Warn("event subscriber too slow, dropping notification", "method", notif.Method)
		}
	}
}

// Call sends a JSON-RPC request and decodes the response into result (which
// may be nil). Typed failures come back as *RPCError; transport problems as
// wrapped standard errors.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	id := strconv.FormatInt(c.nextID.Add(1), 10)
	req := daemon.JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params for %s: %w", method, err)
		}
		req.Params = raw
	}
	idRaw := json.RawMessage(id)
	req.ID = &idRaw

	frame, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request %s: %w", method, err)
	}

	ch := make(chan *daemon.JSONRPCResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	conn, err := c.activeConn(ctx)
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(ctx, callWriteTimeout)
	c.writeMu.Lock()
	err = conn.Write(writeCtx, websocket.MessageText, frame)
	c.writeMu.Unlock()
	cancel()
	if err != nil {
		return fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return &RPCError{Code: resp.Error.Code, Message: resp.Error.Message, Data: resp.Error.Data}
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("decode result of %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("call %s: %w", method, ctx.Err())
	case <-c.done:
		return c.terminalError()
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	notif, err := daemon.NewNotification(method, params)
	if err != nil {
		return fmt.Errorf("build notification %s: %w", method, err)
	}
	frame, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification %s: %w", method, err)
	}

	conn, err := c.activeConn(ctx)
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(ctx, callWriteTimeout)
	c.writeMu.Lock()
	err = conn.Write(writeCtx, websocket.MessageText, frame)
	c.writeMu.Unlock()
	cancel()
	if err != nil {
		return fmt.Errorf("send notification %s: %w", method, err)
	}
	return nil
}

// Events returns a channel receiving daemon notifications. Each call
// registers an independent subscription that detaches when ctx ends and is
// closed when the client finally shuts down. Delivery is best effort:
// overflowed subscriber buffers drop the newest notification.
func (c *Client) Events(ctx context.Context) (<-chan daemon.JSONRPCNotification, error) {
	ch := make(chan daemon.JSONRPCNotification, eventsBuffer)

	c.subsMu.Lock()
	select {
	case <-c.done:
		c.subsMu.Unlock()
		return nil, c.terminalError()
	default:
	}
	c.subs[ch] = struct{}{}
	c.subsMu.Unlock()

	go func() {
		<-ctx.Done()
		c.subsMu.Lock()
		delete(c.subs, ch)
		c.subsMu.Unlock()
	}()
	return ch, nil
}

// failPending errors out every outstanding request (used on connection loss
// and shutdown).
func (c *Client) failPending(reason error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		ch <- &daemon.JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &daemon.JSONRPCError{Code: -32000, Message: reason.Error()},
		}
		delete(c.pending, id)
	}
}

// shutdown marks the client terminated exactly once.
func (c *Client) shutdown(reason error) {
	c.shutdownOnce.Do(func() {
		c.termErr = reason
		c.lifeStop() // cancels in-flight reads/writes derived from lifeCtx
		close(c.done)
	})
}

// isShuttingDown reports whether shutdown has begun.
func (c *Client) isShuttingDown() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// terminalError returns the recorded shutdown cause, defaulting to
// ErrClientClosed.
func (c *Client) terminalError() error {
	if c.termErr != nil {
		return c.termErr
	}
	return ErrClientClosed
}

// Close shuts down the client: it cancels the read pump, closes the
// WebSocket with a bounded handshake, drains the serve loop, and releases
// event subscribers. Safe to call multiple times; later calls return the
// same outcome.
func (c *Client) Close() error {
	c.shutdown(nil)
	<-c.runDone

	c.failPending(c.terminalError())

	c.subsMu.Lock()
	for ch := range c.subs {
		close(ch)
	}
	c.subs = nil
	c.subsMu.Unlock()

	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()
	if conn != nil {
		closeWithDeadline(conn, c.logger)
	}
	return nil
}

// closeWithDeadline performs a graceful WebSocket close handshake bounded by
// closeGracePeriod, then force-drops the socket. On Windows, closing without
// this watchdog can hang past the library's internal timeouts when the peer
// vanished abruptly.
func closeWithDeadline(conn *websocket.Conn, logger *slog.Logger) {
	timer := time.AfterFunc(closeGracePeriod, func() {
		_ = conn.CloseNow()
	})
	defer timer.Stop()
	if err := conn.Close(websocket.StatusNormalClosure, "client shutting down"); err != nil && logger != nil {
		logger.Debug("websocket close handshake ended with error", "error", err)
	}
}

// RPCError is a typed JSON-RPC 2.0 error returned by the daemon.
type RPCError struct {
	Code    int
	Message string
	Data    any
}

// Error implements the error interface with a stable, greppable format.
func (e *RPCError) Error() string {
	if e.Data == nil {
		return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("rpc error %d: %s (%v)", e.Code, e.Message, e.Data)
}

// IsCode reports whether err is an *RPCError carrying the given JSON-RPC
// error code.
func IsCode(err error, code int) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == code
}
