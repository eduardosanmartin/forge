package pluginwasm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// TestUrlcheck_RealFetch verifies the committed urlcheck.wasm loads and executes
// a real net_fetch against an allowlisted httptest server.
func TestUrlcheck_RealFetch(t *testing.T) {
	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	// Allowlist must contain the httptest host (127.0.0.1)
	mgr := NewManager(reg, Options{
		Perms:        engine,
		NetAllowlist: []string{"127.0.0.1"},
		Logger:       slog.Default(),
	})
	defer mgr.Close()

	// Load only urlcheck by using a temp root that is a copy of testdata/urlcheck
	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(tmpRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck")); err != nil {
		t.Fatalf("copy urlcheck: %v", err)
	}

	results, err := mgr.LoadAll(tmpRoot)
	if err != nil {
		t.Fatalf("LoadAll: %v results %+v", err, results)
	}
	if err := mgr.Enable("urlcheck"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	tool, ok := reg.Get("urlcheck_status")
	if !ok {
		t.Fatal("urlcheck_status not registered")
	}
	_ = tool

	ctx := context.Background()
	res, err := reg.Execute(ctx, "urlcheck_status", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v result=%+v", err, res)
	}
	if hit != 1 {
		t.Fatalf("expected 1 hit, got %d", hit)
	}
	// Response should be JSON with url, status, bytes
	inner := unwrapFenced(res.Content)
	var out map[string]any
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		t.Fatalf("response not JSON: %q inner=%q err=%v", res.Content, inner, err)
	}
	if out["url"] != srv.URL {
		t.Errorf("url: got %v want %q", out["url"], srv.URL)
	}
	if int(out["status"].(float64)) != 200 {
		t.Errorf("status: got %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("hello world") {
		t.Errorf("bytes: got %v want %d", out["bytes"], len("hello world"))
	}
}

// TestUrlcheck_NetFetch_Allowlist exercised via the real net_fetch host.
func TestUrlcheck_NetFetch_RedirectAllowlisted(t *testing.T) {
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("final"))
	}))
	defer srv2.Close()

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv2.URL, http.StatusFound)
	}))
	defer srv1.Close()

	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr.Close()

	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(tmpRoot, 0755)
	_ = copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck"))
	if _, err := mgr.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	_ = mgr.Enable("urlcheck")

	res, err := reg.Execute(context.Background(), "urlcheck_status", map[string]any{"url": srv1.URL})
	if err != nil {
		t.Fatalf("Execute redirect allowed: %v", err)
	}
	inner2 := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner2), &out)
	if int(out["status"].(float64)) != 200 {
		t.Errorf("status: got %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("final") {
		t.Errorf("bytes: got %v want %d", out["bytes"], len("final"))
	}
}

func TestUrlcheck_NetFetch_RedirectDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to a host not in allowlist.
		http.Redirect(w, r, "http://example.invalid/denied", http.StatusFound)
	}))
	defer srv.Close()

	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr.Close()

	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(tmpRoot, 0755)
	_ = copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck"))
	if _, err := mgr.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	_ = mgr.Enable("urlcheck")

	res, _ := reg.Execute(context.Background(), "urlcheck_status", map[string]any{"url": srv.URL})
	if !strings.Contains(res.Content, "allowlist") && !strings.Contains(res.Content, "redirect") && !strings.Contains(strings.ToLower(res.Content), "denied") {
		t.Fatalf("expected denied redirect in content, got %q", res.Content)
	}
}

func TestUrlcheck_NetFetch_OversizedTruncates(t *testing.T) {
	const maxBody = 2 * 1024 * 1024
	oversized := strings.Repeat("a", maxBody+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(oversized))
	}))
	defer srv.Close()

	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr.Close()

	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(tmpRoot, 0755)
	_ = copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck"))
	if _, err := mgr.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	_ = mgr.Enable("urlcheck")

	res, err := reg.Execute(context.Background(), "urlcheck_status", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	inner2 := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner2), &out)
	if int(out["bytes"].(float64)) != maxBody {
		t.Errorf("bytes truncated: got %v want %d", out["bytes"], maxBody)
	}
}

func TestUrlcheck_NetFetch_Non2xxReturnsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{Perms: engine, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr.Close()

	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(tmpRoot, 0755)
	_ = copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck"))
	if _, err := mgr.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	_ = mgr.Enable("urlcheck")

	res, err := reg.Execute(context.Background(), "urlcheck_status", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute non-2xx should not be Go error, got %v", err)
	}
	inner2 := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner2), &out)
	if int(out["status"].(float64)) != 404 {
		t.Errorf("status: got %v want 404", out["status"])
	}
	if int(out["bytes"].(float64)) != len("not found") {
		t.Errorf("bytes: got %v want %d", out["bytes"], len("not found"))
	}
}

func TestUrlcheck_PermissionDeniedWithoutNet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ws := t.TempDir()
	// For this test the tool's fs.read permission synthesizes path "./plugin-invoke" which
	// filepath.Abs resolves relative to CWD (package dir), not ws (temp). To make perms allow
	// the placeholder, use an engine whose workspaceRoot is the current package dir and
	// whose policy allows both relative and absolute matches.
	wd, _ := os.Getwd()
	absWd, _ := filepath.Abs(wd)
	permPolicy := permissivePolicy()
	// Add absolute pattern so "./plugin-invoke" (abs = C:/.../plugin-invoke) is allowed.
	permPolicy.FS.Read = append(permPolicy.FS.Read, "C:/**", "/**")
	engine2, _ := perms.New(permPolicy, absWd, slog.Default())
	wasmBytes, _ := os.ReadFile(filepath.Join("testdata", "urlcheck", "urlcheck.wasm"))

	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(filepath.Join(tmpRoot, "urlcheck"), 0755)
	manifest := "name = \"urlcheck\"\nversion = \"0.1.0\"\ndescription = \"no net\"\nsource = \"local\"\nentrypoint = \"urlcheck.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"urlcheck_status\"\ndescription = \"Checks URL status via net_fetch\"\npermission = \"fs.read\"\n"
	_ = os.WriteFile(filepath.Join(tmpRoot, "urlcheck", "manifest.toml"), []byte(manifest), 0644)
	_ = os.WriteFile(filepath.Join(tmpRoot, "urlcheck", "urlcheck.wasm"), wasmBytes, 0644)

	reg2 := tools.New(engine2, ws, slog.Default())
	mgr2 := NewManager(reg2, Options{Perms: engine2, NetAllowlist: []string{"127.0.0.1"}, Logger: slog.Default()})
	defer mgr2.Close()
	if _, err := mgr2.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if err := mgr2.Enable("urlcheck"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	res2, _ := reg2.Execute(context.Background(), "urlcheck_status", map[string]any{"url": srv.URL})
	if !strings.Contains(strings.ToLower(res2.Content), "lacks net capability") && !strings.Contains(res2.Content, "net_fetch denied") {
		t.Fatalf("expected permission denied in content, got %q", res2.Content)
	}
}

// Helper to copy directory for tests.
func copyDirForTest(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDirForTest(s, d); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				return err
			}
			if err := os.WriteFile(d, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ensure import usage for fmt in error messages.
func unwrapFenced(s string) string {
	start := "<CONTENT>\n"
	end := "\n</CONTENT>"
	i := strings.Index(s, start)
	j := strings.Index(s, end)
	if i >= 0 && j > i {
		return s[i+len(start) : j]
	}
	return s
}
var _ = fmt.Sprintf

func TestUrlcheck_TestdataExists(t *testing.T) {
	if _, err := os.Stat("testdata/urlcheck/urlcheck.wasm"); err != nil {
		t.Fatalf("urlcheck.wasm missing: %v", err)
	}
	if _, err := os.Stat("testdata/urlcheck/manifest.toml"); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat("testdata/urlcheck/src/lib.rs"); err != nil {
		t.Fatalf("lib.rs missing (documentation): %v", err)
	}
	if _, err := os.Stat("testdata/urlcheck/Cargo.toml"); err != nil {
		t.Fatalf("Cargo.toml missing: %v", err)
	}
}
