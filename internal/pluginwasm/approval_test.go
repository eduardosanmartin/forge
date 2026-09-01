package pluginwasm

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/tools"
)

func TestManager_Enable_ApprovalRecord(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	// Prepare plugin dir with external manifest and correct checksum
	wasmBytes, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	sum := sha256.Sum256(wasmBytes)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	dir := filepath.Join(pluginsRoot, "greeter")
	_ = os.MkdirAll(dir, 0o755)
	manifest := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"external\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	_ = os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(manifest), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)

	// Case 1: ApproveExternal=false, no approved.flag -> LoadAll should fail (not loaded)
	mgr1 := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: false})
	results, err := mgr1.LoadAll(pluginsRoot)
	if err == nil {
		t.Fatalf("expected load failure without approval, got results %+v", results)
	}
	if len(mgr1.Loaded()) != 0 {
		t.Fatalf("should not be loaded without approval, got %v", mgr1.Loaded())
	}
	_ = mgr1.Close()

	// Case 2: ApproveExternal=false, with approved.flag containing correct hash -> LoadAll succeeds, Enable succeeds
	flagHash := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte(flagHash), 0o644)
	mgr2 := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: false})
	results, err = mgr2.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("load with approved.flag should succeed: %v results %+v", err, results)
	}
	if len(mgr2.Loaded()) != 1 {
		t.Fatalf("expected loaded with flag")
	}
	if err := mgr2.Enable("greeter"); err != nil {
		t.Fatalf("Enable with approved.flag should succeed: %v", err)
	}
	_ = mgr2.Disable("greeter")
	_ = mgr2.Close()

	// Case 2b: record present but artifact bytes swapped -> denied
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), []byte("tampered-bytes"), 0o644)
	mgr2b := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: false})
	results, err = mgr2b.LoadAll(pluginsRoot)
	if err == nil {
		t.Fatalf("expected load failure with mismatched hash, got %+v", results)
	}
	_ = mgr2b.Close()
	// Restore correct wasm bytes and correct flag
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte(flagHash), 0o644)
	// Now test Enable with mismatched flag content
	mgr2c := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: false})
	if _, err := mgr2c.LoadAll(pluginsRoot); err != nil {
		t.Fatalf("load with correct flag should succeed: %v", err)
	}
	// Corrupt flag to wrong hash
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte("sha256:"+strings.Repeat("0", 64)+"\n"), 0o644)
	if err := mgr2c.Enable("greeter"); err == nil || !strings.Contains(err.Error(), "approval record missing or does not match") {
		t.Fatalf("expected approval mismatch on Enable, got %v", err)
	}
	_ = mgr2c.Close()
	_ = os.Remove(filepath.Join(dir, "approved.flag"))
	_ = os.WriteFile(filepath.Join(dir, "greeter.wasm"), wasmBytes, 0o644)

	// Case 3: ApproveExternal=true, no approved.flag -> Enable succeeds without record
	mgr3 := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), ApproveExternal: true})
	results, err = mgr3.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("load with ApproveExternal true should succeed: %v", err)
	}
	if err := mgr3.Enable("greeter"); err != nil {
		t.Fatalf("Enable with ApproveExternal true should succeed without flag: %v", err)
	}
	if err := mgr3.Disable("greeter"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	// Enable again to test double without record still ok
	if err := mgr3.Enable("greeter"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	_ = mgr3.Close()
}

func TestManager_InfoAndReload(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	manifest := "name = \"greeter\"\nversion = \"0.2.0\"\ndescription = \"test greeter\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"Greets\"\npermission = \"fs.read\"\n"
	createPluginDir(t, pluginsRoot, "greeter", manifest, "testdata/greeter/greeter.wasm")

	if _, err := mgr.LoadAll(pluginsRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	infos := mgr.Info()
	if len(infos) != 1 || infos[0].Name != "greeter" || infos[0].Version != "0.2.0" || infos[0].Source != "local" || infos[0].ToolCount != 1 {
		t.Fatalf("Info mismatch: %+v", infos)
	}
	if infos[0].Enabled {
		t.Fatalf("should not be enabled before Enable")
	}
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	infos = mgr.Info()
	if !infos[0].Enabled {
		t.Fatalf("should be enabled after Enable")
	}
	// Reload should re-scan and reset to not enabled (fresh state, local not auto-enabled)
	results, err := mgr.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(results) != 1 || !results[0].Loaded {
		t.Fatalf("Reload results: %+v err %v", results, err)
	}
	infos = mgr.Info()
	if len(infos) != 1 {
		t.Fatalf("Info after reload: %+v", infos)
	}
	if infos[0].Enabled {
		t.Fatalf("after Reload, should be disabled (fresh state)")
	}
	// Verify we can Enable again after reload
	if err := mgr.Enable("greeter"); err != nil {
		t.Fatalf("Enable after reload: %v", err)
	}
}

func TestManager_LoadAll_MissingDirNotError(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default()})
	defer mgr.Close()
	missing := filepath.Join(t.TempDir(), "does-not-exist-12345")
	results, err := mgr.LoadAll(missing)
	if err != nil {
		t.Fatalf("missing dir should not be error, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %v", results)
	}
}

func TestManager_AutoEnableLocal_Reload(t *testing.T) {
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), AutoEnableLocal: true})
	defer mgr.Close()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	// Two locals: greeter (original wasm) and myplugx (patched wasm where tool name is same length)
	wasmBytes, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	// Patch second wasm: replace "greeter_greet" (13 bytes) with "myplugx_greet" (13 bytes) to keep wasm valid
	patchedWasm := make([]byte, len(wasmBytes))
	copy(patchedWasm, wasmBytes)
	patchedWasm = bytesReplace(patchedWasm, []byte("greeter_greet"), []byte("myplugx_greet"))
	manifestGreeter := "name = \"greeter\"\nversion = \"0.1.0\"\ndescription = \"a\"\nsource = \"local\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"greeter_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	manifestMyplugx := "name = \"myplugx\"\nversion = \"0.1.0\"\ndescription = \"b\"\nsource = \"local\"\nentrypoint = \"myplugx.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"myplugx_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	for _, tc := range []struct{ name, manifest, entry string; wasm []byte }{
		{"greeter", manifestGreeter, "greeter.wasm", wasmBytes},
		{"myplugx", manifestMyplugx, "myplugx.wasm", patchedWasm},
	} {
		dir := filepath.Join(pluginsRoot, tc.name)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(tc.manifest), 0o644)
		_ = os.WriteFile(filepath.Join(dir, tc.entry), tc.wasm, 0o644)
	}

	if _, err := mgr.LoadAll(pluginsRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// AutoEnableLocal should have enabled both locals
	if len(mgr.Enabled()) != 2 {
		t.Fatalf("expected 2 enabled after LoadAll with AutoEnableLocal, got %v", mgr.Enabled())
	}
	// Disable one
	if err := mgr.Disable("greeter"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(mgr.Enabled()) != 1 || mgr.Enabled()[0] != "myplugx" {
		t.Fatalf("after Disable greeter, enabled=%v", mgr.Enabled())
	}
	// Reload should re-enable both (policy preserved, state reset)
	if _, err := mgr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	enabled := mgr.Enabled()
	if len(enabled) != 2 {
		t.Fatalf("after Reload with AutoEnableLocal, expected 2 enabled, got %v", enabled)
	}
	// Verify external stays disabled even with AutoEnableLocal true
	wasmBytes2, _ := os.ReadFile("testdata/greeter/greeter.wasm")
	patchedExtWasm := make([]byte, len(wasmBytes2))
	copy(patchedExtWasm, wasmBytes2)
	patchedExtWasm = bytesReplace(patchedExtWasm, []byte("greeter_greet"), []byte("extplug_greet"))
	sum := sha256.Sum256(patchedExtWasm)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	dirExt := filepath.Join(pluginsRoot, "extplug")
	_ = os.MkdirAll(dirExt, 0o755)
	manifestExt := "name = \"extplug\"\nversion = \"0.1.0\"\ndescription = \"ext\"\nsource = \"external\"\nentrypoint = \"greeter.wasm\"\npermissions = [\"fs.read\"]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"extplug_greet\"\ndescription = \"t\"\npermission = \"fs.read\"\n"
	_ = os.WriteFile(filepath.Join(dirExt, "manifest.toml"), []byte(manifestExt), 0o644)
	_ = os.WriteFile(filepath.Join(dirExt, "greeter.wasm"), patchedExtWasm, 0o644)
	// No approved.flag, load should fail for ext even with AutoEnableLocal
	mgr2 := NewManager(reg, Options{Perms: engine, Logger: slog.Default(), AutoEnableLocal: true})
	defer mgr2.Close()
	results, _ := mgr2.LoadAll(pluginsRoot)
	// extplug should not be loaded (approval missing)
	for _, r := range results {
		if r.Name == "extplug" && r.Loaded {
			t.Fatalf("extplug should not be loaded without approval")
		}
	}
	// Now approve extplug with correct hash and reload mgr2
	flag := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
	_ = os.WriteFile(filepath.Join(dirExt, "approved.flag"), []byte(flag), 0o644)
	if _, err := mgr2.LoadAll(pluginsRoot); err != nil {
		// LoadAll will succeed for locals and extplug (now approved)
		// but extplug should still not be auto-enabled
	}
	foundExtEnabled := false
	for _, info := range mgr2.Info() {
		if info.Name == "extplug" && info.Enabled {
			foundExtEnabled = true
		}
	}
	if foundExtEnabled {
		t.Fatalf("external should never be auto-enabled")
	}
}

func bytesReplace(b, old, new []byte) []byte {
	// Simple single replacement for same-length strings
	idx := -1
	for i := 0; i <= len(b)-len(old); i++ {
		match := true
		for j := 0; j < len(old); j++ {
			if b[i+j] != old[j] {
				match = false
				break
			}
		}
		if match {
			idx = i
			break
		}
	}
	if idx >= 0 {
		copy(b[idx:idx+len(new)], new)
	}
	return b
}
