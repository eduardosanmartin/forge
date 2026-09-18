package client

import (
	"net/http"
	"os"

	"github.com/coder/websocket"
)

// WSURL builds the daemon WebSocket URL for addr. The scheme is wss://
// when FORGE_DAEMON_TLS is set (non-empty) — matching a daemon started
// with TLS configured (RF-7.4/RNF-4.11) — and ws:// otherwise, which
// covers every loopback daemon today (unaffected by this feature).
func WSURL(addr string) string {
	scheme := "ws"
	if os.Getenv("FORGE_DAEMON_TLS") != "" {
		scheme = "wss"
	}
	return scheme + "://" + addr + "/ws"
}

// DialOptions returns the websocket.DialOptions every forge client
// connection should dial with. When FORGE_DAEMON_TOKEN is set, it is
// attached as an Authorization: Bearer header so a daemon with auth
// configured (RF-7.4, `forge daemon set-password`) accepts the connection —
// required even for a loopback daemon once a token is configured there,
// since the safety floor only exempts loopback from the *requirement* to
// configure auth, not from checking it when it *is* configured. Returns nil
// (the library's no-options default) when the env var is unset, which is
// every daemon that has never had a password set.
func DialOptions() *websocket.DialOptions {
	tok := os.Getenv("FORGE_DAEMON_TOKEN")
	if tok == "" {
		return nil
	}
	return &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + tok}},
	}
}
