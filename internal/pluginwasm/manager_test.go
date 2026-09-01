package pluginwasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// helper to create a permissive perms engine rooted at dir.
func testEngine(t *testing.T, dir string, policy perms.PermissionsPolicy) *perms.Engine {
	t.Helper()
	e, err := perms.New(policy, dir, slog.Default())
	if err != nil {
		t.Fatalf("perms.New: %v", err)
	}
	return e
}

func permissivePolicy() perms.PermissionsPolicy {
	return perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"echo", "cmd", "powershell"}},
		Git:   perms.GitPermissions{Allow: []string{"status", "log"}},
	}
}

func restrictiveFSReadPolicy() perms.PermissionsPolicy {
	return perms.PermissionsPolicy{
		FS: perms.FSPermissions{Read: []string{"/nope/**"}},
	}
}

// createPluginDir creates a plugin directory under root/<name> with manifest.toml and wasm bytes.
func createPluginDir(t *testing.T, root, name, manifest, wasmPath string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read wasm %q: %v", wasmPath, err)
	}
	// entrypoint is manifest's entrypoint, but for our helpers we always use greeter.wasm
	// Parse manifest to find entrypoint name.
	// Simpler: copy to both greeter.wasm and the manifest's entrypoint if different.
	// We just write greeter.wasm and also try to parse entrypoint via naive search.
	entry := "greeter.wasm"
	if strings.Contains(manifest, "entrypoint = \"bad.wasm\"") {
		entry = "bad.wasm"
	}
	if strings.Contains(manifest, "entrypoint = \"corrupt.wasm\"") {
		entry = "corrupt.wasm"
	}
	if err := os.WriteFile(filepath.Join(dir, entry), wasmBytes, 0644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
}

func TestGreeterWasmTestdataExists(t *testing.T) {
	path := "testdata/greeter/greeter.wasm"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("testdata wasm missing %q: %v", path, err)
	}
	if info.Size() < 1000 {
		t.Fatalf("wasm too small (%d bytes), expected non-trivial", info.Size())
	}
	if _, err := os.Stat("testdata/greeter/main.go"); err != nil {
		t.Fatalf("main.go missing: %v", err)
	}
}

func TestManager_LoadEnableHappyPath(t *testing.T) {
	ws := t.TempDir()
	dataPath := filepath.Join(ws, "data.txt")
	if err := os.WriteFile(dataPath, []byte("sample-content"), 0644); err != nil {
		t.Fatal(err)
	}
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"test greeter\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"Greets\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")

	results, err := mgr.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("LoadAll failed: %v results %+v", err, results)
	}
	if len(mgr.Loaded()) != 1 || mgr.Loaded()[0] != "greeter" {
		t.Fatalf("Loaded = %v, want [greeter]", mgr.Loaded())
	}
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	tool, ok := reg.Get("greeter_greet")
	if !ok {
		t.Fatal("tool greeter_greet not registered after Enable")
	}
	if tool.Name() != "greeter_greet" {
		t.Fatalf("tool name %q", tool.Name())
	}
	ctx := context.Background()
	res, _ := reg.Execute(ctx, "greeter_greet", map[string]any{"name": "world", "file": dataPath})
	if !strings.Contains(res.Content, "hello world") {
		t.Fatalf("execute content missing greeting: %q", res.Content)
	}
	if !strings.Contains(res.Content, "sample-content") {
		t.Fatalf("execute content missing file echo (host import): %q", res.Content)
	}
}

func TestManager_PermissionDenied(t *testing.T) {
	ws := t.TempDir()
	dataPath := filepath.Join(ws, "data.txt")
	os.WriteFile(dataPath, []byte("secret"), 0644)
	// Engine denies fs.read (no matching pattern)
	engine := testEngine(t, ws, restrictiveFSReadPolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"test greeter\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"Greets\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")
	if _, err := mgr.LoadAll(pluginsRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	ctx := context.Background()
	res, _ := reg.Execute(ctx, "greeter_greet", map[string]any{"name": "world", "file": dataPath})
	if !strings.Contains(res.Content, "DENIED") {
		t.Fatalf("expected DENIED when perms not granted, got %q", res.Content)
	}
	// The wasm invocation should not have produced a greeting.
	if strings.Contains(res.Content, "hello world file:secret") {
		t.Fatalf("expected no file echo on denied, got %q", res.Content)
	}
}

func TestManager_ExternalWithoutApproval(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: false})
	defer mgr.Close()

	wasmBytes, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	sum := sha256.Sum256(wasmBytes)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "extplug")
	os.MkdirAll(dir, 0755)
	manifest := "name = \"extplug\"\nversion = \"0.1.0\"\ndescription = \"external\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"extplug_tool\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644)
	os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0644)

	results, err := mgr.LoadAll(pluginsRoot)
	if err == nil {
		t.Fatal("expected fail-closed error for external without approval")
	}
	if !strings.Contains(err.Error(), "requires explicit approval") {
		t.Fatalf("error should mention approval, got %q", err.Error())
	}
	if len(results) == 0 || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "requires explicit approval") {
		t.Fatalf("LoadResult should carry approval error, got %+v", results)
	}
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("external without approval must not be loaded, got %v", mgr.Loaded())
	}
}

func TestManager_ExternalWrongChecksum(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: true})
	defer mgr.Close()

	wasmBytes, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	wrong := "sha256:" + strings.Repeat("0", 64)
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "extbad")
	os.MkdirAll(dir, 0755)
	manifest := "name = \"extbad\"\nversion = \"0.1.0\"\ndescription = \"external\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + wrong + "\"\n\n[[tools]]\nname = \"extbad_tool\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644)
	os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0644)

	_, err := mgr.LoadAll(pluginsRoot)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch before load, got %v", err)
	}
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("wrong checksum must not be loaded")
	}
	_ = wasmBytes // suppress unused
}

func TestManager_ExternalApprovedValidChecksum(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: true})
	defer mgr.Close()

	wasmBytes, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	sum := sha256.Sum256(wasmBytes)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "greeter")
	os.MkdirAll(dir, 0755)
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"external greeter\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644)
	os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0644)

	results, err := mgr.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("expected success with approved valid checksum, got %v results %+v", err, results)
	}
	if len(mgr.Loaded()) != 1 || mgr.Loaded()[0] != "greeter" {
		t.Fatalf("Loaded %v", mgr.Loaded())
	}
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("Enable greeter: %v", err)
	}
	if _, ok := reg.Get("greeter_greet"); !ok {
		t.Fatal("greeter_greet not registered")
	}
}

func TestManager_DisableRemovesTool(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")
	mgr.LoadAll(pluginsRoot)
	mgr.Enable("greeter")
	if _, ok := reg.Get("greeter_greet"); !ok {
		t.Fatal("should be registered")
	}
	if err := mgr.Disable("greeter"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, ok := reg.Get("greeter_greet"); ok {
		t.Fatal("tool should be removed after Disable (Get should fail)")
	}
	// Registry.Execute should now return unknown tool error fenced.
	res, _ := reg.Execute(context.Background(), "greeter_greet", map[string]any{})
	if !strings.Contains(res.Content, "unknown tool") {
		t.Fatalf("expected unknown tool after disable, got %q", res.Content)
	}
}

func TestManager_EnableAgainNoRecompile(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")
	mgr.LoadAll(pluginsRoot)
	mgr.Enable("greeter")
	mgr.Disable("greeter")
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if _, ok := reg.Get("greeter_greet"); !ok {
		t.Fatal("tool should be back after second Enable")
	}
	// execute again end-to-end
	dataPath := filepath.Join(ws, "data.txt")
	os.WriteFile(dataPath, []byte("again"), 0644)
	res, _ := reg.Execute(context.Background(), "greeter_greet", map[string]any{"name": "again", "file": dataPath})
	if !strings.Contains(res.Content, "hello again") {
		t.Fatalf("second enable execute failed: %q", res.Content)
	}
}

func TestManager_ABIVersionMismatch(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "badabi")
	os.MkdirAll(dir, 0755)
	manifest := "name = \"badabi\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"bad.wasm\"\npermissions = []\n"
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644)
	wasmBytes, _ := os.ReadFile("testdata/bad_abi/bad_abi.wasm")
	os.WriteFile(filepath.Join(dir, "bad.wasm"), wasmBytes, 0644)

	_, err := mgr.LoadAll(pluginsRoot)
	if err == nil || !strings.Contains(err.Error(), "ABI version mismatch") {
		t.Fatalf("expected ABI mismatch, got %v", err)
	}
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("bad abi should not be loaded")
	}
}

func TestManager_CorruptedWASM(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "corrupt")
	os.MkdirAll(dir, 0755)
	manifest := "name = \"corrupt\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"corrupt.wasm\"\npermissions = []\n"
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0644)
	os.WriteFile(filepath.Join(dir, "corrupt.wasm"), []byte("not a wasm module \x00\x01"), 0644)

	_, err := mgr.LoadAll(pluginsRoot)
	if err == nil || !strings.Contains(err.Error(), "corrupted wasm") {
		t.Fatalf("expected corrupted wasm error, got %v", err)
	}
	// Must not panic, and Loaded should be empty.
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("corrupted should not be loaded")
	}
}

func TestManager_CloseIdempotent(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")
	mgr.LoadAll(pluginsRoot)
	mgr.Enable("greeter")
	if err := mgr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if _, ok := reg.Get("greeter_greet"); ok {
		t.Fatalf("after Close, plugin tool greeter_greet should be unregistered from the registry")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("second Close (idempotent): %v", err)
	}
	if len(mgr.Loaded()) != 0 {
		t.Fatalf("after close Loaded should be empty, got %v", mgr.Loaded())
	}
}
