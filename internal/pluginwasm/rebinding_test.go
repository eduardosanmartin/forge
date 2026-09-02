package pluginwasm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eduardosanmartin/forge/internal/tools"
)

// fakeResolver allows tests to simulate DNS rebinding without real DNS.
type fakeResolver struct {
	mu       sync.Mutex
	calls    map[string]int
	static   map[string][]net.IP
	errHosts map[string]error
	sequence map[string][][]net.IP // per-host sequence of responses per call
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{
		calls:  make(map[string]int),
		static: make(map[string][]net.IP),
	}
}

func (f *fakeResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errHosts != nil {
		if err, ok := f.errHosts[host]; ok {
			return nil, err
		}
	}
	cnt := f.calls[host]
	f.calls[host] = cnt + 1
	if f.sequence != nil {
		if seq, ok := f.sequence[host]; ok {
			if cnt < len(seq) {
				return append([]net.IP(nil), seq[cnt]...), nil
			}
			// repeat last
			if len(seq) > 0 {
				return append([]net.IP(nil), seq[len(seq)-1]...), nil
			}
		}
	}
	if v, ok := f.static[host]; ok {
		return append([]net.IP(nil), v...), nil
	}
	return nil, fmt.Errorf("fake resolver: no entry for %q", host)
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

// helper to set up a urlcheck plugin manager with given allowlist and resolver.
func setupRebindingManager(t *testing.T, allowlist []string, resolver HostResolver) (*tools.Registry, *Manager, string) {
	t.Helper()
	ws := t.TempDir()
	engine := testEngine(t, ws, permissivePolicy())
	reg := tools.New(engine, ws, slog.Default())
	mgr := NewManager(reg, Options{
		Perms:        engine,
		NetAllowlist: allowlist,
		Logger:       slog.Default(),
		Resolver:     resolver,
	})
	// Copy urlcheck plugin into temp root
	tmpRoot := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(tmpRoot, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := copyDirForTest(filepath.Join("testdata", "urlcheck"), filepath.Join(tmpRoot, "urlcheck")); err != nil {
		t.Fatalf("copy urlcheck: %v", err)
	}
	if _, err := mgr.LoadAll(tmpRoot); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if err := mgr.Enable("urlcheck"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return reg, mgr, ws
}

func executeUrlcheck(t *testing.T, reg *tools.Registry, url string) (tools.Result, error) {
	t.Helper()
	return reg.Execute(context.Background(), "urlcheck_status", map[string]any{"url": url})
}

// (a) rebind simulation → denied
func TestRebinding_Denied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach"))
	}))
	defer srv.Close()
	// Use fake hostname that maps via resolver; URL uses fake host but same port as srv.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	fakeHost := "allowed.test"
	url := fmt.Sprintf("http://%s:%d/", fakeHost, port)

	legit := parseIP("93.184.216.34")
	attacker := parseIP("169.254.169.254")
	fr := newFakeResolver()
	fr.sequence = map[string][][]net.IP{
		fakeHost: {{legit}, {attacker}},
	}
	reg, mgr, _ := setupRebindingManager(t, []string{fakeHost}, fr)
	defer mgr.Close()

	res, _ := executeUrlcheck(t, reg, url)
	// Should be denied (error envelope contains allowlist / resolved IP)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "allowlist") && !strings.Contains(low, "resolved ip") && !strings.Contains(low, "denied") {
		t.Fatalf("expected rebinding denied, got %q", res.Content)
	}
}

// (b) allowlisted host resolving to allowed IP → passes (allowlist containing httptest IP literal)
func TestRebinding_AllowlistedIPLiteralsPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	reg, mgr, _ := setupRebindingManager(t, []string{"127.0.0.1"}, nil)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, srv.URL)
	if err != nil {
		t.Fatalf("Execute: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		t.Fatalf("not json: %q err=%v", inner, err)
	}
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("hello") {
		t.Fatalf("bytes %v want %d", out["bytes"], len("hello"))
	}
}

// (c) redirect to non-allowlisted host → blocked
func TestRebinding_RedirectNonAllowlistedBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/denied", http.StatusFound)
	}))
	defer srv.Close()
	reg, mgr, _ := setupRebindingManager(t, []string{"127.0.0.1"}, nil)
	defer mgr.Close()
	res, _ := executeUrlcheck(t, reg, srv.URL)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "allowlist") && !strings.Contains(low, "redirect") && !strings.Contains(low, "denied") && !strings.Contains(low, "not in allowlist") {
		t.Fatalf("expected redirect denied, got %q", res.Content)
	}
}

// (d) redirect to allowlisted host → passes
func TestRebinding_RedirectAllowlistedPasses(t *testing.T) {
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("final"))
	}))
	defer srv2.Close()
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv2.URL, http.StatusFound)
	}))
	defer srv1.Close()
	reg, mgr, _ := setupRebindingManager(t, []string{"127.0.0.1"}, nil)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, srv1.URL)
	if err != nil {
		t.Fatalf("Execute redirect allowed: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner), &out)
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("final") {
		t.Fatalf("bytes %v want %d", out["bytes"], len("final"))
	}
}

// (e) hostname allowlist entry resolved → its IPs authorize the target (happy path with injected resolver)
func TestRebinding_HostnameAllowlistResolvedAuthorizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Host header preserves original hostname (regression for pinning)
		if r.Host == "" {
			t.Errorf("Host header empty")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	fakeHost := "myhost.test"
	url := fmt.Sprintf("http://%s:%d/", fakeHost, port)
	fr := newFakeResolver()
	fr.static = map[string][]net.IP{
		fakeHost: {parseIP("127.0.0.1")},
	}
	reg, mgr, _ := setupRebindingManager(t, []string{fakeHost}, fr)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, url)
	if err != nil {
		t.Fatalf("Execute: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		t.Fatalf("not json: %q err=%v", inner, err)
	}
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
}

// (f) target resolution failure → denied with clear error
func TestRebinding_TargetResolutionFailureDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	// Use fake host that resolver fails for
	fakeHost := "fail.test"
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/", fakeHost, port)
	fr := newFakeResolver()
	fr.errHosts = map[string]error{fakeHost: fmt.Errorf("dns nxdomain")}
	reg, mgr, _ := setupRebindingManager(t, []string{fakeHost}, fr)
	defer mgr.Close()
	res, _ := executeUrlcheck(t, reg, url)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "dns") && !strings.Contains(low, "resolution") && !strings.Contains(low, "allowlist") {
		t.Fatalf("expected DNS resolution denied, got %q", res.Content)
	}
	// Ensure denied (error envelope surfaces as ERROR: prefix via registry)
	if !strings.Contains(low, "error") && !strings.Contains(low, "denied") {
		t.Fatalf("expected error/denied, got %q", res.Content)
	}
}

// (g) IPv6 literal round-trip
func TestRebinding_IPv6Literal(t *testing.T) {
	// Try to listen on [::1]; skip if not available
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 not available: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ipv6 hello"))
	}))
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	// srv.URL will be http://[::1]:port
	// Allowlist contains ::1 literal (without brackets, as string)
	reg, mgr, _ := setupRebindingManager(t, []string{"::1"}, nil)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, srv.URL)
	if err != nil {
		t.Fatalf("Execute IPv6: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		t.Fatalf("not json: %q err=%v", inner, err)
	}
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("ipv6 hello") {
		t.Fatalf("bytes %v want %d", out["bytes"], len("ipv6 hello"))
	}
}

// (i) empty allowlist still denies everything
func TestRebinding_EmptyAllowlistDenies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	reg, mgr, _ := setupRebindingManager(t, nil, nil)
	defer mgr.Close()
	res, _ := executeUrlcheck(t, reg, srv.URL)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "allowlist") && !strings.Contains(low, "denied") {
		t.Fatalf("expected empty allowlist deny, got %q", res.Content)
	}
}

// Strict all-of: target resolves to two IPs, one disallowed → denied
func TestRebinding_StrictAllOfMixedIPsDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mixed"))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	fakeHost := "mixed.test"
	url := fmt.Sprintf("http://%s:%d/", fakeHost, port)
	legit := parseIP("127.0.0.1")
	attacker := parseIP("10.0.0.1")
	fr := newFakeResolver()
	// Target returns both IPs; allowlist entry returns only legit
	fr.sequence = map[string][][]net.IP{
		fakeHost: {{legit, attacker}, {legit}},
	}
	// Allowlist is same host, but we arrange second call (allowlist) returns only legit.
	// First call (target) returns legit+attacker → strict should deny because attacker not allowed.
	reg, mgr, _ := setupRebindingManager(t, []string{fakeHost}, fr)
	defer mgr.Close()
	res, _ := executeUrlcheck(t, reg, url)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "allowlist") && !strings.Contains(low, "resolved ip") && !strings.Contains(low, "denied") {
		t.Fatalf("expected strict mixed IPs denied, got %q", res.Content)
	}
}

// CIDR allowlist authorizes IP within range
func TestRebinding_CIDRAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cidr ok"))
	}))
	defer srv.Close()
	// srv is 127.0.0.1, CIDR 127.0.0.0/8 should allow it
	reg, mgr, _ := setupRebindingManager(t, []string{"127.0.0.0/8"}, nil)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, srv.URL)
	if err != nil {
		t.Fatalf("CIDR Execute: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner), &out)
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
}

// CIDR that does NOT contain target IP should deny
func TestRebinding_CIDRDeniesOutsideRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cidr deny"))
	}))
	defer srv.Close()
	reg, mgr, _ := setupRebindingManager(t, []string{"10.0.0.0/8"}, nil)
	defer mgr.Close()
	res, _ := executeUrlcheck(t, reg, srv.URL)
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "allowlist") && !strings.Contains(low, "resolved ip") && !strings.Contains(low, "denied") {
		t.Fatalf("expected CIDR deny, got %q", res.Content)
	}
}

// Redirect to hostname allowlisted via resolver
func TestRebinding_RedirectHostnameViaResolver(t *testing.T) {
	finalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("redirect final"))
	}))
	defer finalSrv.Close()
	finalPort := finalSrv.Listener.Addr().(*net.TCPAddr).Port

	fakeHost := "redirect.test"
	redirectURL := fmt.Sprintf("http://%s:%d/final", fakeHost, finalPort)

	startSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectURL, http.StatusFound)
	}))
	defer startSrv.Close()

	fr := newFakeResolver()
	fr.static = map[string][]net.IP{
		fakeHost: {parseIP("127.0.0.1")},
	}
	// Both start and final need allowlist: start is 127.0.0.1, redirect is fakeHost
	reg, mgr, _ := setupRebindingManager(t, []string{"127.0.0.1", fakeHost}, fr)
	defer mgr.Close()
	res, err := executeUrlcheck(t, reg, startSrv.URL)
	if err != nil {
		t.Fatalf("redirect via resolver Execute: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner), &out)
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
	if int(out["bytes"].(float64)) != len("redirect final") {
		t.Fatalf("bytes %v want %d", out["bytes"], len("redirect final"))
	}
}

// Suffix match still works (api.example.com allowed by example.com) with IP pinning
func TestRebinding_SuffixMatchWithResolver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("suffix"))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	// Allowlist contains example.com, target is api.example.com
	// Both resolve to 127.0.0.1 via fake resolver
	fr := newFakeResolver()
	fr.static = map[string][]net.IP{
		"example.com":     {parseIP("127.0.0.1")},
		"api.example.com": {parseIP("127.0.0.1")},
	}
	reg, mgr, _ := setupRebindingManager(t, []string{"example.com"}, fr)
	defer mgr.Close()
	url := fmt.Sprintf("http://api.example.com:%d/", port)
	res, err := executeUrlcheck(t, reg, url)
	if err != nil {
		t.Fatalf("suffix Execute: %v res=%q", err, res.Content)
	}
	inner := unwrapFenced(res.Content)
	var out map[string]any
	_ = json.Unmarshal([]byte(inner), &out)
	if int(out["status"].(float64)) != 200 {
		t.Fatalf("status %v want 200", out["status"])
	}
}
