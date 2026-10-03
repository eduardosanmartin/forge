// Package webui embeds the forge session GUI (RF-7.2/7.3): a static,
// build-free HTML/CSS/JS client served by the daemon's own HTTP mux
// alongside the existing /ws and /health routes. It talks to the daemon
// exclusively over the same JSON-RPC-over-WebSocket API the CLI/TUI use;
// it adds no new backend surface, no auth, and no listener changes.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// Handler returns an http.Handler serving the embedded GUI assets rooted
// at "/". Callers mount it on their own mux; it does not register routes
// itself so it cannot shadow sibling routes like /ws or /health.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(sub))
	// Explicit content types: the default comes from the OS (on Windows the
	// registry), which made .js "application/javascript" on one machine and
	// "text/javascript" on another (caught by CI, 2026-10-03).
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct, ok := contentTypes[strings.ToLower(path.Ext(r.URL.Path))]; ok {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	}), nil
}

var contentTypes = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".svg":  "image/svg+xml",
}
