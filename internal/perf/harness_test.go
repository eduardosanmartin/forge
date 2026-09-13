package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/logging"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// perfLogger returns a quiet structured logger for benchmark runs.
func perfLogger(t testing.TB) *slog.Logger {
	t.Helper()
	logger, file, err := logging.New(logging.Config{Level: "error"})
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	if file != nil {
		t.Cleanup(func() { _ = file.Close() })
	}
	return logger
}

// tempWorkspace returns a fresh working-directory tree for one benchmark
// run: it mimics the directory the daemon would have been launched from.
func tempWorkspace(t testing.TB) string {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return workspace
}

// llmChatMessage and friends mirror the OpenAI chat-completion wire shape
// with pointer-free local types so the perf mock server stays independent
// of the llm package test fixtures.
type llmChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmChatChoice struct {
	Index        int            `json:"index"`
	Message      llmChatMessage `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type llmChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type llmChatCompletion struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []llmChatChoice `json:"choices"`
	Usage   *llmChatUsage   `json:"usage,omitempty"`
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("perf: mock response marshal: %v", err))
	}
	return b
}

// cannedChatCompletionBody is the zero-latency canned OpenAI-format chat
// completion every perf mock server answers with (same shape as
// internal/llm/ollama_test.go's ChatResponseBuilder output).
var cannedChatCompletionBody = mustJSON(llmChatCompletion{
	ID:    "chatcmpl-perf",
	Model: "perf-mock",
	Choices: []llmChatChoice{{
		Index: 0,
		Message: llmChatMessage{
			Role:    "assistant",
			Content: "ok",
		},
		FinishReason: "stop",
	}},
	Usage: &llmChatUsage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10},
})

// newMockChatServer starts a local httptest server that answers every
// request (chat completions, model listing, ...) with the same canned
// OpenAI-format chat completion. With zero added latency, the harness
// overhead measured downstream is pure forge code.
func newMockChatServer(t testing.TB) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cannedChatCompletionBody)
	}))
	t.Cleanup(server.Close)
	return server
}

// perfConfig returns the default forge configuration with the ollama
// provider's base URL pointed at the mock server, so the benchmarks
// exercise the real llm.New + provider construction path.
func perfConfig(serverURL string) *config.Config {
	cfg := config.Defaults()
	// httptest URLs have no path; the provider defaults it to /v1.
	prov := cfg.Providers["ollama"]
	prov.BaseURL = serverURL
	cfg.Providers["ollama"] = prov
	return cfg
}

// perfPermissionsPolicy mirrors what cli.runServe assembles: a generous
// fs/shell/git allow set so startup work is exercised instead of
// permission refusals.
func perfPermissionsPolicy() perms.PermissionsPolicy {
	return perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"*"}},
		Git:   perms.GitPermissions{Allow: []string{"*"}},
	}
}

// startDaemonInProcess replicates the internal/cli runServe startup
// sequence unchanged — perms.New -> store.Open -> llm.New ->
// embedding.NewStore -> retrieval.NewRetriever -> compaction.NewCompactor
// -> anchor table + NewAnchorStoreSQL -> skill.NewManager (+ scan) ->
// agent.V1Deps -> tools.NewDefaultRegistryWithDeps -> pluginwasm.NewManager
// (+ load) -> daemon.New -> daemon.Start — on a fresh temp workspace state,
// with the LLM provider pointed at mockURL. It returns after
// daemon.IsRunning() reports ready. Only signal handling (process level,
// unrepresentable inside a test binary) is omitted.
func startDaemonInProcess(t testing.TB, ctx context.Context, mockURL string) (*daemon.Daemon, func()) {
	t.Helper()
	logger := perfLogger(t)
	cfg := perfConfig(mockURL)
	workspace := tempWorkspace(t)

	// Permission engine.
	permsEng, err := perms.New(perfPermissionsPolicy(), workspace, logger)
	if err != nil {
		t.Fatalf("create permission engine: %v", err)
	}

	// Store on a fresh temp database path.
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// LLM registry through the real llm.New construction path, against
	// the mock inference server.
	//
	// NOTE: intra-process start timings below assume the provider
	// constructor's startup model fetch succeeds quickly against the
	// localhost mock; warnings are tolerated like runServe does.
	llmReg, err := llm.New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("create llm registry: %v", err)
	}

	// v1 feature dependencies, in runServe order.
	embStore, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("create embedding store: %v", err)
	}
	retriever := retrieval.NewRetriever(embStore)
	compactor := compaction.NewCompactor(compaction.Config{SummaryCharsPerMessage: 60})
	if err := anchor.CreateAnchorTable(ctx, st.DB()); err != nil {
		t.Fatalf("create anchors table: %v", err)
	}
	anchorStore := anchor.NewAnchorStoreSQL(st.DB())
	// Skills manager: owns its own embedding store internally; a missing
	// directory is NOT an error (runServe only warns).
	skillsMgr := skill.NewManager(skill.Options{Logger: logger, ApproveExternal: false})
	if results, scanErr := skillsMgr.Scan(filepath.Join(workspace, ".forge", "skills")); scanErr != nil {
		t.Logf("skills scan warning (expected on empty workspace): %v (%v)", scanErr, results)
	}
	v1Deps := agent.V1Deps{
		Retriever:   retriever,
		Compactor:   compactor,
		AnchorStore: anchorStore,
		Skills:      skillsMgr,
	}

	// Tools registry: base five tools plus the six v1 feature tools.
	toolsReg := tools.NewDefaultRegistryWithDeps(permsEng, workspace, logger, retriever, compactor, anchorStore)

	// Plugin manager: WASM runtime bridging tools; a missing directory is
	// NOT an error (runServe only warns).
	pluginMgr := pluginwasm.NewManager(toolsReg, pluginwasm.Options{
		Perms:           permsEng,
		NetAllowlist:    cfg.Network.AllowedHosts,
		Logger:          logger,
		ApproveExternal: false,
		AutoEnableLocal: true,
	})
	if _, perr := pluginMgr.LoadAll(filepath.Join(workspace, "forge-plugins")); perr != nil {
		t.Logf("plugins scan warning (expected on empty workspace): %v", perr)
	}

	// Daemon.
	d, err := daemon.New(cfg, st, llmReg, toolsReg, permsEng, logger, "127.0.0.1:0", v1Deps, pluginMgr, skillsMgr)
	if err != nil {
		t.Fatalf("create daemon: %v", err)
	}

	// Start blocks until ctx is cancelled; run it in a goroutine and wait
	// for IsRunning() to report ready.
	started := make(chan error, 1)
	go func() { started <- d.Start(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d.IsRunning() {
			break
		}
		select {
		case err := <-started:
			t.Fatalf("daemon stopped before reaching ready state: %v", err)
		case <-time.After(2 * time.Millisecond):
		}
	}
	if !d.IsRunning() {
		t.Fatalf("daemon did not become ready within 10s")
	}

	cleanup := func() {
		// d.Stop closes the store and the LLM registry on the running
		// path; the auxiliary owners below are closed idempotently.
		_ = d.Stop()
		_ = pluginMgr.Close()
		_ = skillsMgr.Close()
		_ = embStore.Close()
	}
	return d, cleanup
}
