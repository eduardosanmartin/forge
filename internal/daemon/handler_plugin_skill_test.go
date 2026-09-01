package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func newTestHandlerWithManagers(t *testing.T) (*Handler, *pluginwasm.Manager, *skill.Manager) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	permEng, _ := perms.New(perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"echo"}},
		Git:   perms.GitPermissions{Allow: []string{"status"}},
	}, t.TempDir(), logger)

	toolsReg := tools.New(permEng, t.TempDir(), logger)
	emergency := NewEmergencyState(logger)
	mgr := NewSessionManager(st, nil, toolsReg, emergency, logger, &config.Config{}, permEng, st)

	// Plugin manager
	pluginMgr := pluginwasm.NewManager(toolsReg, pluginwasm.Options{Perms: permEng, Logger: logger, ApproveExternal: true})
	t.Cleanup(func() { pluginMgr.Close() })
	// Skill manager
	skillMgr := skill.NewManager(skill.Options{Logger: logger, ApproveExternal: true})
	t.Cleanup(func() { skillMgr.Close() })

	handler := NewHandler(mgr, logger, pluginMgr, skillMgr)
	return handler, pluginMgr, skillMgr
}

func TestHandler_PluginListEmpty(t *testing.T) {
	h, _, _ := newTestHandlerWithManagers(t)
	req := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginList}
	resp := h.HandleRequest(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("expected success, got error %v", resp.Error)
	}
	var result PluginListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Plugins) != 0 {
		t.Fatalf("expected 0 plugins, got %v", result.Plugins)
	}
}

func TestHandler_PluginEnableDisableFlow(t *testing.T) {
	h, pluginMgr, _ := newTestHandlerWithManagers(t)
	// Create a local plugin and load it
	ws := t.TempDir()
	pluginRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"test\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"g\"\npermission = \"fs.read\"\n"
	// Use helper from pluginwasm tests: create dir manually
	dir := filepath.Join(pluginRoot, "greeter")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0o644)
	wasmBytes, _ := os.ReadFile("../pluginwasm/testdata/greeter/greeter.wasm")
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)
	_ = ws
	if _, err := pluginMgr.LoadAll(pluginRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// List should show disabled initially
	reqList := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginList}
	resp := h.HandleRequest(context.Background(), reqList)
	var listRes PluginListResult
	_ = json.Unmarshal(resp.Result, &listRes)
	if len(listRes.Plugins) != 1 || listRes.Plugins[0].Enabled {
		t.Fatalf("expected 1 disabled plugin, got %+v", listRes)
	}
	// Enable via RPC
	enableParams, _ := json.Marshal(PluginEnableParams{Name: "greeter"})
	reqEn := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginEnable, Params: enableParams}
	resp = h.HandleRequest(context.Background(), reqEn)
	if resp.Error != nil {
		t.Fatalf("enable failed: %v", resp.Error)
	}
	// Enable again should be already enabled
	resp = h.HandleRequest(context.Background(), reqEn)
	if resp.Error == nil || resp.Error.Code != ErrCodeAlreadyEnabled {
		t.Fatalf("expected already enabled, got %+v", resp.Error)
	}
	// Disable
	disableParams, _ := json.Marshal(PluginDisableParams{Name: "greeter"})
	reqDis := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginDisable, Params: disableParams}
	resp = h.HandleRequest(context.Background(), reqDis)
	if resp.Error != nil {
		t.Fatalf("disable failed: %v", resp.Error)
	}
	// Disable again should be not enabled
	resp = h.HandleRequest(context.Background(), reqDis)
	if resp.Error == nil || resp.Error.Code != ErrCodeNotEnabled {
		t.Fatalf("expected not enabled, got %+v", resp.Error)
	}
	// Enable non-existent -> not loaded
	enableParams2, _ := json.Marshal(PluginEnableParams{Name: "nope"})
	reqEn2 := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginEnable, Params: enableParams2}
	resp = h.HandleRequest(context.Background(), reqEn2)
	if resp.Error == nil || resp.Error.Code != ErrCodeNotLoaded {
		t.Fatalf("expected not loaded, got %+v", resp.Error)
	}
}

func TestHandler_PluginEnable_ApprovalRequired(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	st, _ := store.Open(":memory:")
	defer st.Close()
	permEng, _ := perms.New(perms.PermissionsPolicy{FS: perms.FSPermissions{Read: []string{"./**"}}}, t.TempDir(), logger)
	toolsReg := tools.New(permEng, t.TempDir(), logger)
	emergency := NewEmergencyState(logger)
	mgr := NewSessionManager(st, nil, toolsReg, emergency, logger, &config.Config{}, permEng, st)
	pluginMgr := pluginwasm.NewManager(toolsReg, pluginwasm.Options{Perms: permEng, Logger: logger, ApproveExternal: false})
	defer pluginMgr.Close()
	skillMgr := skill.NewManager(skill.Options{Logger: logger})
	defer skillMgr.Close()
	h := NewHandler(mgr, logger, pluginMgr, skillMgr)

	// Create external plugin with approved flag missing, but load it with global false should fail to load
	// So we create it with ApproveExternal false: LoadAll will fail. To test Enable approval, we need to load with flag present then remove flag before enable.
	pluginRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginRoot, "greeter")
	_ = os.MkdirAll(dir, 0o755)
	wasmBytes, _ := os.ReadFile("../pluginwasm/testdata/greeter/greeter.wasm")
	sum := sha256.Sum256(wasmBytes)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"external\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	_ = os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)
	flagHash := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte(flagHash), 0o644)
	if _, err := pluginMgr.LoadAll(pluginRoot); err != nil {
		t.Fatalf("LoadAll with flag: %v", err)
	}
	// Remove flag to simulate missing approval before enable
	_ = os.Remove(filepath.Join(dir, "approved.flag"))
	enableParams, _ := json.Marshal(PluginEnableParams{Name: "greeter"})
	reqEn := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginEnable, Params: enableParams}
	resp := h.HandleRequest(context.Background(), reqEn)
	if resp.Error == nil || resp.Error.Code != ErrCodeApprovalRequired {
		t.Fatalf("expected approval required, got %+v", resp.Error)
	}
}

func TestHandler_SkillFlows(t *testing.T) {
	h, _, skillMgr := newTestHandlerWithManagers(t)
	skillRoot := filepath.Join(t.TempDir(), "skills")
	dir := filepath.Join(skillRoot, "my-skill")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: \"desc\"\nsource: local\n---\nBody\n"), 0o644)
	if _, err := skillMgr.Scan(skillRoot); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// List
	reqList := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSkillList}
	resp := h.HandleRequest(context.Background(), reqList)
	if resp.Error != nil {
		t.Fatalf("skill list: %v", resp.Error)
	}
	var listRes SkillListResult
	_ = json.Unmarshal(resp.Result, &listRes)
	if len(listRes.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %+v", listRes)
	}
	// Local auto-enabled, so Enable should be already enabled
	enableParams, _ := json.Marshal(SkillEnableParams{Name: "my-skill"})
	reqEn := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSkillEnable, Params: enableParams}
	resp = h.HandleRequest(context.Background(), reqEn)
	if resp.Error == nil || resp.Error.Code != ErrCodeAlreadyEnabled {
		t.Fatalf("expected already enabled, got %+v", resp.Error)
	}
	// Disable
	disableParams, _ := json.Marshal(SkillDisableParams{Name: "my-skill"})
	reqDis := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSkillDisable, Params: disableParams}
	resp = h.HandleRequest(context.Background(), reqDis)
	if resp.Error != nil {
		t.Fatalf("disable: %v", resp.Error)
	}
	// Enable after disable should succeed
	resp = h.HandleRequest(context.Background(), reqEn)
	if resp.Error != nil {
		t.Fatalf("enable after disable: %v", resp.Error)
	}
	// Reload
	reqReload := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodSkillReload}
	resp = h.HandleRequest(context.Background(), reqReload)
	if resp.Error != nil {
		t.Fatalf("reload: %v", resp.Error)
	}
	var reloadRes SkillReloadResult
	_ = json.Unmarshal(resp.Result, &reloadRes)
	if len(reloadRes.Results) != 1 || !reloadRes.Results[0].Loaded {
		t.Fatalf("reload results: %+v", reloadRes)
	}
}

func TestHandler_ReloadPlugin(t *testing.T) {
	h, pluginMgr, _ := newTestHandlerWithManagers(t)
	pluginRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginRoot, "greeter")
	_ = os.MkdirAll(dir, 0o755)
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"test\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"g\"\npermission = \"fs.read\"\n"
	_ = os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0o644)
	wasmBytes, _ := os.ReadFile("../pluginwasm/testdata/greeter/greeter.wasm")
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)
	if _, err := pluginMgr.LoadAll(pluginRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	reqReload := &JSONRPCRequest{JSONRPC: "2.0", Method: MethodPluginReload}
	resp := h.HandleRequest(context.Background(), reqReload)
	if resp.Error != nil {
		t.Fatalf("plugin reload: %v", resp.Error)
	}
	var res PluginReloadResult
	_ = json.Unmarshal(resp.Result, &res)
	if len(res.Results) != 1 || !res.Results[0].Loaded {
		t.Fatalf("reload plugin results: %+v", res)
	}
}
