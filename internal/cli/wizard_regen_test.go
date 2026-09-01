package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/pluginwasm"
	"github.com/eduardosanmartin/forge/internal/tools"
)

func discoverCargoForTest() string {
	if p := os.Getenv("FORGE_CARGO"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("cargo"); err == nil {
		return p
	}
	fallback := `C:\Users\eduar\.cargo\bin\cargo.exe`
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	return ""
}

func TestWizard_RegenAndCargoBuild(t *testing.T) {
	cargoPath := discoverCargoForTest()
	if cargoPath == "" {
		t.Skip("LOUD: cargo not found — skipping regen test (install Rust 1.98+ and ensure cargo is on PATH or at C:\\Users\\eduar\\.cargo\\bin\\cargo.exe, or set FORGE_CARGO). The committed urlcheck.wasm keeps the load invariant tested without cargo (see internal/pluginwasm/urlcheck_test.go).")
	}
	if out, err := exec.Command(cargoPath, "--version").CombinedOutput(); err != nil {
		t.Skipf("LOUD: cargo not runnable (%v, output %q) — skipping regen", err, string(out))
	} else {
		t.Logf("cargo: %s (%s)", cargoPath, strings.TrimSpace(string(out)))
	}

	tmp := t.TempDir()
	pluginsRoot := filepath.Join(tmp, "forge-plugins")
	prompter := NewScriptedPrompter([]string{
		"regen_check",
		"0.1.0",
		"regen test",
		"n", "n", "n", "n", "y",
		"",
		"local",
	})
	var out bytes.Buffer
	if err := runPluginWizard(prompter, &out, pluginsRoot, false); err != nil {
		t.Fatalf("runPluginWizard: %v out=%s", err, out.String())
	}
	t.Logf("wizard: %s", out.String())

	plugDir := filepath.Join(pluginsRoot, "regen_check")
	if _, err := os.Stat(filepath.Join(plugDir, "manifest.toml")); err != nil {
		t.Fatalf("manifest not generated: %v", err)
	}

	fixedLibPath := filepath.Join("..", "pluginwasm", "testdata", "urlcheck", "src", "lib.rs")
	fixedLib, err := os.ReadFile(fixedLibPath)
	if err != nil {
		t.Fatalf("read fixed lib.rs: %v", err)
	}
	fixedStr := strings.ReplaceAll(string(fixedLib), "urlcheck_status", "regen_check_hello")
	if err := os.WriteFile(filepath.Join(plugDir, "src", "lib.rs"), []byte(fixedStr), 0644); err != nil {
		t.Fatalf("write fixed lib.rs: %v", err)
	}

	cargoArgs := []string{"build", "--target", "wasm32-unknown-unknown", "--release", "--manifest-path", filepath.Join(plugDir, "Cargo.toml")}
	cmd := exec.Command(cargoPath, cargoArgs...)
	var cargoOut bytes.Buffer
	cmd.Stdout = &cargoOut
	cmd.Stderr = &cargoOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("cargo build failed: %v output:\n%s", err, cargoOut.String())
	}
	t.Logf("cargo output tail:\n%s", tailForTest(cargoOut.String(), 500))

	builtWasm := filepath.Join(plugDir, "target", "wasm32-unknown-unknown", "release", "regen_check.wasm")
	if _, err := os.Stat(builtWasm); err != nil {
		alt := filepath.Join("..", "..", "target", "wasm32-unknown-unknown", "release", "regen_check.wasm")
		if _, err2 := os.Stat(alt); err2 == nil {
			builtWasm = alt
		} else {
			t.Fatalf("built wasm not found at %q: %v", builtWasm, err)
		}
	}
	data, err := os.ReadFile(builtWasm)
	if err != nil {
		t.Fatalf("read wasm: %v", err)
	}
	entryPath := filepath.Join(plugDir, "plugin.wasm")
	if err := os.WriteFile(entryPath, data, 0644); err != nil {
		t.Fatalf("copy wasm: %v", err)
	}
	t.Logf("wasm %d bytes", len(data))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("regen ok"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	permEngine, err := perms.New(perms.PermissionsPolicy{
		FS:    perms.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: perms.ShellPermissions{Allow: []string{"echo"}},
		Git:   perms.GitPermissions{Allow: []string{"status"}},
	}, ws, slog.Default())
	if err != nil {
		t.Fatalf("perms.New: %v", err)
	}
	reg := tools.New(permEngine, ws, slog.Default())
	mgr := pluginwasm.NewManager(reg, pluginwasm.Options{Perms: permEngine, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr.Close()

	results, err := mgr.LoadAll(pluginsRoot)
	if err != nil {
		t.Fatalf("LoadAll: %v results %+v", err, results)
	}
	if err := mgr.Enable("regen_check"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	res, err := reg.Execute(context.Background(), "regen_check_hello", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	inner := unwrapFencedCLI(res.Content)
	var outMap map[string]any
	if err := json.Unmarshal([]byte(inner), &outMap); err != nil {
		t.Fatalf("response not JSON: %q inner=%q", res.Content, inner)
	}
	if int(outMap["status"].(float64)) != 200 {
		t.Errorf("status: %v", outMap["status"])
	}
	if int(outMap["bytes"].(float64)) != len("regen ok") {
		t.Errorf("bytes: %v want %d", outMap["bytes"], len("regen ok"))
	}
}

func unwrapFencedCLI(s string) string {
	start := "<CONTENT>\n"
	end := "\n</CONTENT>"
	i := strings.Index(s, start)
	j := strings.Index(s, end)
	if i >= 0 && j > i {
		return s[i+len(start) : j]
	}
	return s
}

func tailForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
