package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/approval"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func locateMockProviderWasm(t *testing.T) ([]byte, string) {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "pluginwasm", "testdata", "mock-provider", "mock_provider.wasm"),
		filepath.Join("..", "..", "internal", "pluginwasm", "testdata", "mock-provider", "mock_provider.wasm"),
	}
	_, thisFile, _, _ := runtime.Caller(0)
	thisDir := filepath.Dir(thisFile)
	candidates = append(candidates, filepath.Join(thisDir, "..", "pluginwasm", "testdata", "mock-provider", "mock_provider.wasm"))
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			abs, _ := filepath.Abs(p)
			return data, abs
		}
	}
	t.Fatalf("committed mock_provider.wasm not found (tried %v)", candidates)
	return nil, ""
}

func prepareExternalProviderSource(t *testing.T, wasmBytes []byte) string {
	t.Helper()
	parent := t.TempDir()
	src := filepath.Join(parent, "mock_provider")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir provider src: %v", err)
	}
	checksum := wasmChecksumHex(wasmBytes)
	manifest := fmt.Sprintf("name = \"mock_provider\"\nversion = \"0.1.0\"\ndescription = \"Mock LLM provider dogfood\"\nsource = \"external\"\nentrypoint = \"mock_provider.wasm\"\nkind = \"provider\"\npermissions = [\"llm\"]\nchecksum = %q\n", checksum)
	if err := os.WriteFile(filepath.Join(src, "manifest.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "mock_provider.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	return src
}

// TestProviderStreaming_AgentTurn verifies the WU2 streaming bridge end-to-end:
// a provider-plugin (mock_provider, ABI v2) is loaded as an external plugin with
// v2 signed approval (isolated FORGE_KEYS_DIR), registered into the LLM path as
// a provider named by manifest, and an agent completes ONE full turn through the
// real loop with streaming enabled — parity with WU3 consumeStream path.
func TestProviderStreaming_AgentTurn(t *testing.T) {
	// Skip if mock wasm not present (cargo build only for committed artifact regen).
	wasmBytes, wasmPath := locateMockProviderWasm(t)
	t.Logf("mock_provider wasm: %s (%d bytes)", wasmPath, len(wasmBytes))

	keysDir := t.TempDir()
	t.Setenv("FORGE_KEYS_DIR", keysDir)
	if _, _, err := approval.Keygen(false); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	priv, err := approval.LoadPrivateKey()
	if err != nil {
		t.Fatalf("load private key: %v", err)
	}

	// Prepare external provider source (simulates third-party download).
	src := prepareExternalProviderSource(t, wasmBytes)
	// Install via direct approval WriteV2 (isolated, no cargo needed).
	pluginsRoot := filepath.Join(t.TempDir(), "forge-plugins")
	dest := filepath.Join(pluginsRoot, "mock_provider")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	// Copy manifest+wasm.
	for _, f := range []string{"manifest.toml", "mock_provider.wasm"} {
		data, _ := os.ReadFile(filepath.Join(src, f))
		if err := os.WriteFile(filepath.Join(dest, f), data, 0o644); err != nil {
			t.Fatalf("copy %s: %v", f, err)
		}
	}
	checksum := wasmChecksumHex(wasmBytes)
	if err := approval.WriteV2(dest, "plugin", "mock_provider", checksum, priv, 0); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}
	if err := approval.VerifyFile(dest, "plugin", "mock_provider", checksum, slog.Default()); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	wsPermsDir := t.TempDir()
	permEngine, err := perms.New(perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"echo"}},
		Git:   perms.GitPermissions{Allow: []string{"status"}},
	}, wsPermsDir, slog.Default())
	if err != nil {
		t.Fatalf("perms.New: %v", err)
	}
	toolsReg := tools.New(permEngine, wsPermsDir, slog.Default())
	pluginMgr := pluginwasm.NewManager(toolsReg, pluginwasm.Options{
		Perms:           permEngine,
		Logger:          slog.Default(),
		ApproveExternal: false,
	})
	defer pluginMgr.Close()

	results, err := pluginMgr.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("LoadAll: %v results %+v", err, results)
	}
	if len(pluginMgr.Loaded()) != 1 || pluginMgr.Loaded()[0] != "mock_provider" {
		t.Fatalf("Loaded = %v want [mock_provider]", pluginMgr.Loaded())
	}
	if err := pluginMgr.Enable("mock_provider"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	prov, ok := pluginMgr.GetLLMProvider("mock_provider")
	if !ok {
		t.Fatalf("GetLLMProvider failed")
	}

	registry := &stubRegistry{prov: prov, model: "mock_provider"}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := config.Defaults()
	cfg.LLM.Streaming = config.StreamingConfig{Mode: config.StreamingModeOn}
	logger := slog.Default()
	ag := agent.NewAgent(cfg, st, registry, toolsReg, permEngine, logger)

	// Capture deltas via OnDelta to prove streaming path was exercised.
	var deltas []string
	onDelta := func(d string) { deltas = append(deltas, d) }

	res, err := ag.ExecuteTurnWithOptions(ctx, sess.ID, "hello", agent.TurnOptions{StreamingEnabled: true, OnDelta: onDelta})
	if err != nil {
		t.Fatalf("ExecuteTurnWithOptions: %v err %+v", err, res)
	}
	if len(res.Messages) == 0 {
		t.Fatalf("no messages returned")
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last message role %q want assistant", last.Role)
	}
	if !strings.Contains(last.Content, "Hello from mock provider!") {
		t.Fatalf("assistant content %q does not contain expected streaming reply; deltas %v", last.Content, deltas)
	}
	// Parity: deltas should reconstruct the final content (WU3 consumeStream contract).
	if len(deltas) == 0 {
		t.Fatalf("expected deltas via streaming, got none")
	}
	joined := strings.Join(deltas, "")
	if joined != "Hello from mock provider!" {
		t.Fatalf("joined deltas %q want %q", joined, "Hello from mock provider!")
	}
	// Verify the wasm on disk was not rebuilt.
	installedWasm, _ := os.ReadFile(filepath.Join(dest, "mock_provider.wasm"))
	if string(installedWasm) != string(wasmBytes) {
		t.Fatalf("installed wasm was mutated")
	}
	_ = wasmPath
}

// stubRegistry is a tiny LLMRegistryInterface for agent tests, backed by a provider-plugin.
type stubRegistry struct {
	prov  llm.Provider
	model string
}

func (s *stubRegistry) GetDefault() (llm.Provider, string) { return s.prov, s.model }
func (s *stubRegistry) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	return s.prov.Chat(ctx, req)
}

var _ agent.LLMRegistryInterface = (*stubRegistry)(nil)
