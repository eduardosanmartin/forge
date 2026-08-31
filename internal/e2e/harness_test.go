package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// ---------------------------------------------------------------------------
// Shared in-process stack: every real forge component except the LLM itself,
// connected through the production WebSocket transport and client.
// ---------------------------------------------------------------------------

// stack is one fully wired forge instance scoped to a temp workspace.
type stack struct {
	t         *testing.T
	workspace string
	cfg       *config.Config
	transport *daemon.Transport
	client    *client.Client
}

// testPolicy mirrors the shipped default posture (deny-by-default, workspace
// fs access, conventional git allowlist) with one test-scoped adjustment:
// shell.allow is ["go"] per the E2E design. require_isolation is left at the
// config-level secure default; on Linux CI without the isolation wrapper the
// offline suite would refuse shell_exec, so the stack builder disables it
// explicitly (OS-isolation behavior itself is covered by internal/tools
// shell_isolation_test.go).
func testPolicy() perms.PermissionsPolicy {
	return perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"go"}},
		Git:   perms.GitPermissions{Allow: []string{"status", "add", "commit", "log", "diff", "branch", "switch", "stash", "restore", "show", "remote", "fetch"}},
	}
}

// initialWd captures the process working directory once so stack cleanups can
// restore it after chdirIntoWorkspace.
var initialWd = sync.OnceValue(func() string {
	wd, err := os.Getwd()
	if err != nil {
		panic("e2e: resolve working directory: " + err.Error())
	}
	return wd
})

// ownTempDir creates a temp directory managed by this suite instead of
// t.TempDir. On Windows, antivirus/indexer processes transiently hold open
// handles on freshly written trees, which makes the framework's immediate
// RemoveAll fail ("being used by another process") and fails the whole test.
// The bounded retry absorbs that external hazard; a leftover temp dir on a
// hard cleanup failure is reported but never affects test outcomes.
func ownTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix+"-*")
	if err != nil {
		t.Fatalf("create temp dir %s: %v", prefix, err)
	}
	t.Cleanup(func() {
		const attempts = 20
		for i := 0; i < attempts; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Logf("warning: could not remove temp dir %s after %d attempts (external handle holder)", dir, attempts)
	})
	return dir
}

// newStack builds a complete in-process forge stack against baseURL (an
// OpenAI-compatible endpoint) advertising models. The caller receives a
// connected client; everything is cleaned up via t.Cleanup.
//
// The process working directory is moved into the temp workspace for the
// duration of the stack: native tools resolve relative paths against the
// process CWD, and in production (forge serve) the permission engine's
// workspace root IS that launch directory. Chdir keeps both consistent and
// mirrors real operation.
func newStack(t *testing.T, baseURL string, models []string) *stack {
	t.Helper()

	ws := ownTempDir(t, "forge-e2e-ws")
	initGitRepo(t, ws)

	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir into workspace %s: %v", ws, err)
	}
	// Registered last => runs first on unwind, before temp dirs are removed
	// (Windows cannot delete a directory that is a process's CWD).
	t.Cleanup(func() { _ = os.Chdir(initialWd()) })

	cfg := config.Defaults()
	cfg.Storage.Path = filepath.Join(ownTempDir(t, "forge-e2e-home"), "forge.db")
	cfg.DefaultProvider = "ollama"
	cfg.Providers = map[string]config.Provider{
		"ollama": {Kind: "openai-compatible", BaseURL: baseURL, Models: models},
	}
	cfg.Network.AllowedHosts = []string{"127.0.0.1", "localhost"}
	pol := testPolicy()
	cfg.Permissions = config.PermissionsPolicy{
		FS:    config.FSPermissions{Read: pol.FS.Read, Write: pol.FS.Write},
		Shell: config.ShellPermissions{Allow: pol.Shell.Allow, RequireIsolation: false},
		Git:   config.GitPermissions{Allow: pol.Git.Allow},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	st, err := store.Open(cfg.Storage.Path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	llmReg, err := llm.New(cfg, cfg.Network.AllowedHosts, discardLogger())
	if err != nil {
		t.Fatalf("create llm registry: %v", err)
	}
	t.Cleanup(func() { _ = llmReg.Close() })

	permsEng, err := perms.New(pol, ws, nil)
	if err != nil {
		t.Fatalf("create perms engine: %v", err)
	}

	toolsReg := tools.NewDefaultRegistry(permsEng, ws, discardLogger())

	emergency := daemon.NewEmergencyState(discardLogger())
	mgr := daemon.NewSessionManager(st, llmReg, toolsReg, emergency, discardLogger(), cfg, permsEng, st)
	handler := daemon.NewHandler(mgr, discardLogger())
	tx := daemon.NewTransport("127.0.0.1:0", handler, discardLogger())
	if err := tx.Start(context.Background()); err != nil {
		t.Fatalf("start transport: %v", err)
	}
	t.Cleanup(func() { _ = tx.Stop() })

	cl, err := client.Connect(context.Background(), tx.Addr())
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	return &stack{t: t, workspace: ws, cfg: cfg, transport: tx, client: cl}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// createSession creates a session through the RPC surface.
func (s *stack) createSession() string {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var res daemon.SessionResult
	if err := s.client.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{
		Metadata: map[string]any{"source": "e2e"},
	}, &res); err != nil {
		s.t.Fatalf("create session: %v", err)
	}
	return res.ID
}

// call performs one bounded RPC call against the stack's client.
func (s *stack) call(method string, params, result any) error {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Call(ctx, method, params, result)
}

// executeTurn runs one agent turn and returns the result plus wall-clock
// duration in milliseconds. A non-nil RPC error fails the test immediately.
func (s *stack) executeTurn(sessionID, prompt string) (daemon.ExecuteTurnResult, int64) {
	s.t.Helper()
	start := time.Now()
	// Generous per-turn bound: local 7B models may spend minutes across
	// several tool-call iterations; each individual LLM call stays capped by
	// the provider's own HTTP timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	var res daemon.ExecuteTurnResult
	err := s.client.Call(ctx, daemon.MethodExecuteTurn,
		daemon.ExecuteTurnParams{SessionID: sessionID, UserMessage: prompt}, &res)
	if err != nil {
		s.t.Fatalf("execute turn %q: %v", truncate(prompt, 60), err)
	}
	return res, time.Since(start).Milliseconds()
}

// fullTranscript returns every message stored for sessionID in chronological
// order (the RPC returns ascending seq already).
func (s *stack) fullTranscript(sessionID string) []daemon.MessageResult {
	s.t.Helper()
	var res daemon.GetMessagesResult
	if err := s.call(daemon.MethodGetMessagesSince,
		daemon.GetMessagesSinceParams{SessionID: sessionID, SinceSeq: 0}, &res); err != nil {
		s.t.Fatalf("get messages since 0: %v", err)
	}
	return res.Messages
}

// ---------------------------------------------------------------------------
// Turn analysis helpers shared by both suites.
// ---------------------------------------------------------------------------

// turnStat is the per-turn evidence row printed by the sustained tests.
type turnStat struct {
	index            int
	durationMs       int64
	promptTokens     int
	completionTokens int
	tools            []string
	finalLen         int
}

// summarizeTurnResult derives the evidence row from an ExecuteTurnResult:
// token totals are summed across ALL assistant messages of the turn (a
// multi-iteration tool turn records usage on each assistant message), which
// makes the >0 assertion independent of where the provider reports usage.
func summarizeTurnResult(index int, res daemon.ExecuteTurnResult, durationMs int64) turnStat {
	stat := turnStat{index: index, durationMs: durationMs, finalLen: len(res.FinalContent)}
	for _, tc := range res.ToolTrace {
		stat.tools = append(stat.tools, tc.Name)
	}
	for _, m := range res.Messages {
		if m.Role == "assistant" && m.Usage != nil {
			stat.promptTokens += m.Usage.PromptTokens
			stat.completionTokens += m.Usage.CompletionTokens
		}
	}
	return stat
}

// assertTurnSane enforces the per-turn regression locks: strictly positive
// decoded token usage (the wire-format fix regression lock — zero usage means
// snake_case decoding broke again), a non-empty final answer for pure
// conversational turns, and required tools present in the trace.
//
// A tool-work turn MAY end with an empty final text: some local models emit
// an empty content alongside their closing stop after completing tool calls.
// For those turns the outcome checks (spec.check) are authoritative, not the
// prose.
func assertTurnSane(t *testing.T, stat turnStat, requiredTools ...string) {
	t.Helper()
	if stat.finalLen == 0 {
		if len(stat.tools) == 0 {
			t.Errorf("turn %d: final assistant content is empty", stat.index)
		} else {
			t.Logf("turn %d: final content empty on tool-work turn (outcome checks are authoritative)", stat.index)
		}
	}
	if stat.promptTokens <= 0 || stat.completionTokens <= 0 {
		t.Errorf("turn %d: token usage must be positive (prompt=%d completion=%d) — wire-format regression?",
			stat.index, stat.promptTokens, stat.completionTokens)
	}
	for _, want := range requiredTools {
		found := false
		for _, got := range stat.tools {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("turn %d: expected tool %q in trace, got %v", stat.index, want, stat.tools)
		}
	}
}

// logStats prints the per-turn metrics table as test evidence.
func logStats(t *testing.T, label string, stats []turnStat) {
	t.Helper()
	t.Logf("%s — per-turn metrics:", label)
	for _, s := range stats {
		t.Logf("  turn %d: dur=%dms prompt_tok=%d completion_tok=%d tools=%v",
			s.index, s.durationMs, s.promptTokens, s.completionTokens, s.tools)
	}
}

// assertNoDegradation is the long-session degradation SMOKE check (explicitly
// not a benchmark): a turn fails only if it is BOTH more than 4x slower than
// the slowest previous turn AND more than 90 seconds absolute, or if any turn
// exceeds a hard cap sized to tolerate a cold multi-iteration tool turn on a
// local 7B model. The generous bounds absorb normal local-model variance
// while still catching structural degradation such as unbounded context growth.
func assertNoDegradation(t *testing.T, stats []turnStat) {
	t.Helper()
	const hardCapMs = 420_000
	var maxSoFar int64
	for _, s := range stats {
		if maxSoFar > 0 && s.durationMs > 4*maxSoFar && s.durationMs > 90_000 {
			t.Errorf("turn %d took %dms, more than 4x the previous maximum %dms (and >90s): degradation smoke tripped",
				s.index, s.durationMs, maxSoFar)
		}
		if s.durationMs > hardCapMs {
			t.Errorf("turn %d took %dms, above the %dms hard cap", s.index, s.durationMs, hardCapMs)
		}
		if s.durationMs > maxSoFar {
			maxSoFar = s.durationMs
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// Git helpers for the temp workspace repos.
// ---------------------------------------------------------------------------

// initGitRepo prepares dir as a git repo with a local identity so commits in
// the tests never depend on machine-global git configuration.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "e2e@forge.local")
	runGit(t, dir, "config", "user.name", "Forge E2E")
}

// runGit runs git in dir and fails the test on error, returning combined output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Scripted OpenAI-compatible mock server for the offline suite.
//
// Replicates the pattern of internal/llm/mockserver_test.go (that helper
// lives in the llm package's test files and cannot be imported cross-package):
// raw wire-format JSON bodies served from an ordered script; the last step
// repeats once the script is exhausted.
// ---------------------------------------------------------------------------

type scriptStep struct {
	delay time.Duration
	body  string
}

type scriptServer struct {
	srv    *httptest.Server
	model  string
	mu     sync.Mutex
	steps  []scriptStep
	idx    int
	chats  atomic.Int32
	bodies muSlice[string]
}

// newScriptServer starts a mock serving GET /models and POST /chat/completions.
func newScriptServer(t *testing.T, model string, steps ...scriptStep) *scriptServer {
	t.Helper()
	if len(steps) == 0 {
		t.Fatal("script server needs at least one step")
	}
	s := &scriptServer{model: model, steps: steps}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scriptServer) handle(w http.ResponseWriter, req *http.Request) {
	// Normalize the optional OpenAI-compat "/v1" prefix (the provider adds
	// it when the configured base URL has no path).
	path := strings.TrimPrefix(req.URL.Path, "/v1")
	switch {
	case req.Method == http.MethodGet && path == "/models":
		writeJSON(w, map[string]any{"models": []map[string]any{{"name": s.model}}})
	case req.Method == http.MethodPost && path == "/chat/completions":
		raw, _ := readAll(req)
		s.bodies.append(string(raw))
		s.chats.Add(1)

		s.mu.Lock()
		step := s.steps[min(s.idx, len(s.steps)-1)]
		s.idx++
		s.mu.Unlock()

		if step.delay > 0 {
			time.Sleep(step.delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(step.body))
	default:
		http.NotFound(w, req)
	}
}

func (s *scriptServer) URL() string             { return s.srv.URL }
func (s *scriptServer) chatCount() int          { return int(s.chats.Load()) }
func (s *scriptServer) requestBodies() []string { return s.bodies.snapshot() }

// respTool builds one scripted response carrying tool calls.
func respTool(calls ...llm.ToolCall) scriptStep {
	return respMsg("", calls, "tool_calls")
}

// respFinal builds one scripted terminal assistant answer.
func respFinal(content string) scriptStep {
	return respMsg(content, nil, "stop")
}

var scriptSeq atomic.Int32

func respMsg(content string, calls []llm.ToolCall, finish string) scriptStep {
	n := scriptSeq.Add(1)
	resp := llm.ChatResponse{
		ID:    fmt.Sprintf("chatcmpl-e2e-%d", n),
		Model: "mock-7b",
		Choices: []llm.Choice{{
			Index:        0,
			Message:      llm.Message{Role: "assistant", Content: content, ToolCalls: calls},
			FinishReason: finish,
		}},
		Usage: &llm.Usage{PromptTokens: 100 + int(n), CompletionTokens: 10 + int(n), TotalTokens: 110 + 2*int(n)},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		panic(fmt.Sprintf("marshal scripted response: %v", err))
	}
	return scriptStep{body: string(body)}
}

// delayed wraps a step with an artificial server-side latency (used to keep a
// turn reliably in flight while the halt arrives).
func delayed(d time.Duration, s scriptStep) scriptStep {
	s.delay = d
	return s
}

// toolCall builds one OpenAI-shaped function call with marshaled args.
func toolCall(id, name string, args any) llm.ToolCall {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(fmt.Sprintf("marshal tool args: %v", err))
	}
	return llm.ToolCall{ID: id, Type: "function",
		Function: llm.ToolCallFunction{Name: name, Arguments: string(raw)}}
}

// ---------------------------------------------------------------------------
// Small stdlib-only utilities (no external dependencies allowed).
// ---------------------------------------------------------------------------

type muSlice[T any] struct {
	mu sync.Mutex
	v  []T
}

func (m *muSlice[T]) append(v T) {
	m.mu.Lock()
	m.v = append(m.v, v)
	m.mu.Unlock()
}

func (m *muSlice[T]) snapshot() []T {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]T, len(m.v))
	copy(out, m.v)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(v)
	_, _ = w.Write(data)
}

func readAll(req *http.Request) ([]byte, error) {
	return io.ReadAll(req.Body)
}

// envOrDefault returns env[key] when set, fallback otherwise.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// contextWithTimeout wraps context.WithTimeout for call sites that only need
// the duration.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
