// Package mcpbridge connects forge to external MCP servers (F6): each
// allowed tool of an approved server is registered in forge's tools
// registry as "mcp__<server>__<tool>", so it goes through the same
// permission engine (kind "mcp", deny-by-default), output fencing,
// secret redaction and prompt-injection flagging as forge's own tools.
//
// Provenance (RNF-4.6): a server's tool list — names, descriptions and
// schemas, which reach the model's context and are therefore untrusted —
// is hashed; a server is used only after a human approved that exact hash
// (`forge mcp approve <server>`). A changed list ("rug pull") disables the
// server again until re-approved.
package mcpbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolPrefix starts every bridged tool's name.
const ToolPrefix = "mcp__"

// connectTimeout bounds starting a server and listing its tools.
const connectTimeout = 30 * time.Second

var nameRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// ToolName is the registry name of an MCP tool.
func ToolName(server, tool string) string {
	return ToolPrefix + nameRe.ReplaceAllString(server, "_") + "__" + nameRe.ReplaceAllString(tool, "_")
}

// Server is one live connection.
type Server struct {
	Name    string
	session *mcp.ClientSession
	Tools   []*mcp.Tool
	Hash    string
}

// Connect starts/opens the server and lists its tools.
func Connect(ctx context.Context, name string, spec config.MCPServer, allowedHosts []string) (*Server, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	var transport mcp.Transport
	switch {
	case spec.Command != "" && spec.URL != "":
		return nil, fmt.Errorf("mcp server %q: set either command or url, not both", name)
	case spec.Command != "":
		cmd := exec.Command(spec.Command, spec.Args...)
		cmd.Env = os.Environ()
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+expandEnv(v))
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case spec.URL != "":
		if err := checkHost(spec.URL, allowedHosts); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		transport = &mcp.StreamableClientTransport{Endpoint: spec.URL, HTTPClient: &http.Client{Timeout: 5 * time.Minute}}
	default:
		return nil, fmt.Errorf("mcp server %q: needs a command or a url", name)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "forge", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp server %q: connect: %w", name, err)
	}
	var list []*mcp.Tool
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			return nil, fmt.Errorf("mcp server %q: list tools: %w", name, err)
		}
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return &Server{Name: name, session: session, Tools: list, Hash: HashTools(list)}, nil
}

// Close ends the session (and the server process, for stdio servers).
func (s *Server) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

// HashTools fingerprints what a server exposes to the model.
func HashTools(list []*mcp.Tool) string {
	type fp struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      any    `json:"schema"`
	}
	fps := make([]fp, 0, len(list))
	for _, t := range list {
		fps = append(fps, fp{t.Name, t.Description, t.InputSchema})
	}
	data, _ := json.Marshal(fps)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Approvals stores the approved tool-list hash per server.
type Approvals struct {
	path string
	mu   sync.Mutex
	m    map[string]string
}

// LoadApprovals reads path (missing file = no approvals).
func LoadApprovals(path string) (*Approvals, error) {
	a := &Approvals{path: path, m: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return a, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &a.m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// Approved reports whether hash is the approved hash of server.
func (a *Approvals) Approved(server, hash string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[server] == hash
}

// Approve records hash for server and saves.
func (a *Approvals) Approve(server, hash string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[server] = hash
	data, err := json.MarshalIndent(a.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.path, append(data, '\n'), 0o600)
}

// Manager connects the configured servers and registers their tools.
type Manager struct {
	cfg       *config.Config
	approvals *Approvals
	perms     *perms.Engine
	logger    *slog.Logger

	mu      sync.Mutex
	servers map[string]*Server
}

// NewManager builds a manager; nothing connects until Start.
func NewManager(cfg *config.Config, approvals *Approvals, engine *perms.Engine, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{cfg: cfg, approvals: approvals, perms: engine, logger: logger, servers: map[string]*Server{}}
}

// Start connects every configured server and registers the tools that
// the policy allows (or asks for) into reg. Unapproved or changed servers
// are skipped with a warning. Meant to run in the background after the
// daemon is listening (each server is a process start / network call).
func (m *Manager) Start(ctx context.Context, reg *tools.Registry) {
	names := make([]string, 0, len(m.cfg.MCP.Servers))
	for n := range m.cfg.MCP.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		srv, err := Connect(ctx, name, m.cfg.MCP.Servers[name], m.cfg.Network.AllowedHosts)
		if err != nil {
			m.logger.Warn("mcp server unavailable", "server", name, "error", err)
			continue
		}
		if !m.approvals.Approved(name, srv.Hash) {
			m.logger.Warn("mcp server not approved (new, or its tool list changed): run `forge mcp approve "+name+"` after reviewing `forge mcp list`", "server", name)
			srv.Close()
			continue
		}
		registered := 0
		for _, t := range srv.Tools {
			if !m.exposed(name, t.Name) {
				continue
			}
			reg.Register(&bridgedTool{server: srv, tool: t})
			registered++
		}
		m.mu.Lock()
		m.servers[name] = srv
		m.mu.Unlock()
		m.logger.Info("mcp server connected", "server", name, "tools", len(srv.Tools), "exposed", registered)
	}
}

// exposed reports whether the policy allows or asks for server/tool:
// tools nobody may use are not shown to the model at all (each schema
// costs prompt tokens on every request).
func (m *Manager) exposed(server, tool string) bool {
	if m.perms == nil {
		return false
	}
	d := m.perms.Evaluate(perms.Request{Kind: perms.KindMCP, Command: server + "/" + tool})
	return d.Allowed || d.Ask
}

// Close disconnects every server.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		_ = s.Close()
	}
	m.servers = map[string]*Server{}
}

// bridgedTool is one MCP tool as a forge tool.
type bridgedTool struct {
	server *Server
	tool   *mcp.Tool
}

func (t *bridgedTool) Name() string { return ToolName(t.server.Name, t.tool.Name) }

func (t *bridgedTool) Description() string {
	return fmt.Sprintf("[MCP server %q] %s", t.server.Name, t.tool.Description)
}

func (t *bridgedTool) JSONSchema() map[string]any {
	if m, ok := t.tool.InputSchema.(map[string]any); ok {
		return m
	}
	return map[string]any{"type": "object"}
}

// PermsRequest gates the call as kind "mcp", "server/tool".
func (t *bridgedTool) PermsRequest(args map[string]any) (perms.Request, error) {
	return perms.Request{Kind: perms.KindMCP, Command: t.server.Name + "/" + t.tool.Name, Input: args}, nil
}

func (t *bridgedTool) Execute(ctx context.Context, req perms.Request) (tools.Result, error) {
	res, err := t.server.session.CallTool(ctx, &mcp.CallToolParams{Name: t.tool.Name, Arguments: req.Input})
	if err != nil {
		return tools.Result{}, fmt.Errorf("mcp %s/%s: %w", t.server.Name, t.tool.Name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			sb.WriteString(v.Text)
		default:
			fmt.Fprintf(&sb, "[non-text content: %T]", c)
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 && res.StructuredContent != nil {
		data, _ := json.Marshal(res.StructuredContent)
		sb.Write(data)
	}
	out := strings.TrimRight(sb.String(), "\n")
	if res.IsError {
		out = "ERROR: " + out
	}
	return tools.Result{Content: out, Metadata: map[string]any{"mcp_server": t.server.Name, "mcp_tool": t.tool.Name}}, nil
}

// expandEnv resolves a "${env:NAME}" value from forge's environment.
func expandEnv(v string) string {
	if name, ok := strings.CutPrefix(v, "${env:"); ok && strings.HasSuffix(name, "}") {
		return os.Getenv(strings.TrimSuffix(name, "}"))
	}
	return v
}

func checkHost(raw string, allowed []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url %q", raw)
	}
	host := u.Hostname()
	for _, a := range allowed {
		if strings.EqualFold(a, host) || strings.EqualFold(a, u.Host) {
			return nil
		}
		if h, _, err := net.SplitHostPort(a); err == nil && strings.EqualFold(h, host) && strings.EqualFold(a, u.Host) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not in network.allowed_hosts (RNF-4.9)", u.Host)
}
