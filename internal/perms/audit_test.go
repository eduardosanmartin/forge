package perms

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// newAuditHarness returns an engine wired to a plain stdlib JSON handler
// (NOT forge's redacting wrapper) plus the buffer it writes to. Redaction in
// the emitted records can therefore only come from the engine itself.
func newAuditHarness(t *testing.T, f func(*PermissionsPolicy)) (*Engine, string, *bytes.Buffer) {
	t.Helper()
	root := testWorkspaceRoot(t)
	policy := PermissionsPolicy{
		FS:    FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: ShellPermissions{Allow: []string{"curl"}},
		Git:   GitPermissions{Allow: []string{"status", "push"}},
	}
	if f != nil {
		f(&policy)
	}
	buf := &bytes.Buffer{}
	eng, err := New(policy, root, slog.New(slog.NewJSONHandler(buf, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng, root, buf
}

// decodeSingleRecord parses the one expected JSON line in buf.
func decodeSingleRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 audit record, got %d: %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode audit record %q: %v", lines[0], err)
	}
	return rec
}

func str(t *testing.T, rec map[string]any, key string) string {
	t.Helper()
	v, ok := rec[key].(string)
	if !ok {
		t.Fatalf("record %v missing string attr %q", rec, key)
	}
	return v
}

func TestAuditRecordShapeAndCommonAttrs(t *testing.T) {
	cases := []struct {
		name      string
		req       Request
		wantKind  string
		wantAllow bool
		wantRule  string
	}{
		{
			name:      "fs read",
			req:       Request{Kind: KindFsRead},
			wantKind:  "fs.read",
			wantAllow: true,
			wantRule:  "fs.read:./**",
		},
		{
			name:      "shell miss",
			req:       Request{Kind: KindShell, Command: "evil"},
			wantKind:  "shell.exec",
			wantAllow: false,
			wantRule:  "default-deny:" + string(KindShell),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, root, buf := newAuditHarness(t, nil)
			req := tc.req
			if req.Kind == KindFsRead {
				req.Path = filepath.Join(root, "sub", "f.txt")
			}
			eng.Check(req)
			rec := decodeSingleRecord(t, buf)

			if msg := str(t, rec, "msg"); msg != "perm.check" {
				t.Errorf("msg = %q, want %q", msg, "perm.check")
			}
			if kind := str(t, rec, "kind"); kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if allowed, ok := rec["allowed"].(bool); !ok || allowed != tc.wantAllow {
				t.Errorf("allowed = %v (%T), want %v", rec["allowed"], rec["allowed"], tc.wantAllow)
			}
			if rule := str(t, rec, "rule"); rule != tc.wantRule {
				t.Errorf("rule = %q, want %q", rule, tc.wantRule)
			}
			if tc.wantKind == string(KindFsRead) {
				if path := str(t, rec, "path"); path != "sub/f.txt" {
					t.Errorf(`path = %q, want forward-slashed workspace-relative "sub/f.txt"`, path)
				}
			}
		})
	}
}

func TestAuditRedactsSecretsInShellArgs(t *testing.T) {
	const secret = "SUPERSECRET123456" // >=8 chars so the generic assignment rule fires

	eng, _, buf := newAuditHarness(t, nil)
	eng.Check(Request{
		Kind:    KindShell,
		Command: "curl",
		Args:    []string{"-H", "api_key=" + secret, "https://example.com"},
	})

	out := buf.String()
	rec := decodeSingleRecord(t, buf)
	args := str(t, rec, "args")

	if strings.Contains(out, secret) {
		t.Errorf("raw secret leaked into audit output: %q", out)
	}
	if !strings.Contains(args, "[REDACTED]") {
		t.Errorf("args %q lost the redaction placeholder", args)
	}
	if command := str(t, rec, "command"); command != "curl" {
		t.Errorf("command = %q, want base name %q", command, "curl")
	}
}

func TestAuditTruncatesLongJoinedArgs(t *testing.T) {
	longArg := strings.Repeat("a", 300)

	eng, _, buf := newAuditHarness(t, nil)
	eng.Check(Request{Kind: KindShell, Command: "curl", Args: []string{longArg}})

	rec := decodeSingleRecord(t, buf)
	args := str(t, rec, "args")
	if got := len([]rune(args)); got > 200 {
		t.Errorf("args length = %d runes, want <= 200 (truncated)", got)
	}
	if !strings.HasPrefix(longArg, args) {
		t.Errorf("truncation did not preserve the prefix: got %q...", args)
	}
}

func TestAuditGitRecordShape(t *testing.T) {
	eng, _, buf := newAuditHarness(t, nil)
	eng.Check(Request{Kind: KindGit, Subcommand: "push", GitArgs: []string{"origin", "main"}})

	rec := decodeSingleRecord(t, buf)
	if sub := str(t, rec, "subcommand"); sub != "push" {
		t.Errorf("subcommand = %q, want %q", sub, "push")
	}
	if args := str(t, rec, "args"); args != "origin main" {
		t.Errorf("args = %q, want %q", args, "origin main")
	}
}

func TestNilLoggerEmitsNothingAndNeverPanics(t *testing.T) {
	root := testWorkspaceRoot(t)
	eng, err := New(PermissionsPolicy{}, root, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := eng.Check(Request{Kind: KindShell, Command: "go"}) // must not panic
	if d.Allowed {
		t.Errorf("unexpected allow with empty policy: %+v", d)
	}
}
