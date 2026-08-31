package perms

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// testWorkspaceRoot returns a fresh absolute workspace directory.
func testWorkspaceRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}
	return root
}

// newTestEngine builds an Engine over a fresh workspace with the default
// example policy, optionally mutated by f before validation.
func newTestEngine(t *testing.T, f func(*PermissionsPolicy)) (*Engine, string) {
	t.Helper()
	root := testWorkspaceRoot(t)
	policy := PermissionsPolicy{
		FS:    FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: ShellPermissions{Allow: []string{}},
		Git:   GitPermissions{Allow: []string{"status", "add", "commit", "log", "diff", "branch", "switch", "stash", "restore", "show", "remote", "fetch"}},
	}
	if f != nil {
		f(&policy)
	}
	eng, err := New(policy, root, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng, root
}

func TestNewRejectsInvalidConstruction(t *testing.T) {
	base := PermissionsPolicy{}

	cases := []struct {
		name     string
		root     string
		useTmp   bool
		mutate   func(*PermissionsPolicy)
		wantErrs []string
	}{
		{
			name:     "relative workspace root",
			root:     "relative/root",
			wantErrs: []string{"absolute"},
		},
		{
			name:     "empty workspace root",
			root:     "",
			wantErrs: []string{"absolute"},
		},
		{
			name:   "invalid fs.read pattern",
			useTmp: true,
			mutate: func(p *PermissionsPolicy) {
				p.FS.Read = []string{`a\b`}
			},
			wantErrs: []string{"permissions.fs.read[0]"},
		},
		{
			name:   "invalid fs.write pattern",
			useTmp: true,
			mutate: func(p *PermissionsPolicy) {
				p.FS.Write = []string{"../up"}
			},
			wantErrs: []string{"permissions.fs.write[0]", ".."},
		},
		{
			name:   "empty shell allow entry",
			useTmp: true,
			mutate: func(p *PermissionsPolicy) {
				p.Shell.Allow = []string{"go", " "}
			},
			wantErrs: []string{"permissions.shell.allow[1]"},
		},
		{
			name:   "empty git allow entry",
			useTmp: true,
			mutate: func(p *PermissionsPolicy) {
				p.Git.Allow = []string{""}
			},
			wantErrs: []string{"permissions.git.allow[0]"},
		},
		{
			name:   "empty custom deny entry",
			useTmp: true,
			mutate: func(p *PermissionsPolicy) {
				p.Custom.Deny = []string{"go", " "}
			},
			wantErrs: []string{"permissions.custom.deny[1]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root
			if tc.useTmp {
				root = testWorkspaceRoot(t)
			}
			policy := base
			policy.FS.Read = []string{}
			policy.FS.Write = []string{}
			policy.Shell.Allow = []string{}
			policy.Git.Allow = []string{}
			if tc.mutate != nil {
				tc.mutate(&policy)
			}
			_, err := New(policy, root, nil)
			if err == nil {
				t.Fatal("New succeeded; want validation error")
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestMalformedRequestsDenied(t *testing.T) {
	eng, _ := newTestEngine(t, nil)

	cases := []struct {
		name string
		req  Request
	}{
		{name: "empty kind", req: Request{}},
		{name: "unknown kind string", req: Request{Kind: Kind("net.http"), Command: "curl"}},
		{name: "fs.read without path", req: Request{Kind: KindFsRead}},
		{name: "fs.write without path", req: Request{Kind: KindFsWrite}},
		{name: "shell without command", req: Request{Kind: KindShell, Args: []string{"x"}}},
		{name: "git without subcommand", req: Request{Kind: KindGit, GitArgs: []string{"origin"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(tc.req)
			if d.Allowed {
				t.Errorf("request %+v allowed; want malformed denial", tc.req)
			}
			if d.Rule != "malformed-request" {
				t.Errorf("Rule = %q, want \"malformed-request\"", d.Rule)
			}
		})
	}
}

func TestDefaultDenyWithEmptyPolicies(t *testing.T) {
	eng, root := newTestEngine(t, func(p *PermissionsPolicy) {
		p.FS.Read = []string{}
		p.FS.Write = []string{}
		p.Shell.Allow = []string{}
		p.Git.Allow = []string{}
	})

	cases := []struct {
		req      Request
		wantRule string
	}{
		{req: Request{Kind: KindFsRead, Path: filepath.Join(root, "a.txt")}, wantRule: "default-deny:" + string(KindFsRead)},
		{req: Request{Kind: KindFsWrite, Path: filepath.Join(root, "a.txt")}, wantRule: "default-deny:" + string(KindFsWrite)},
		{req: Request{Kind: KindShell, Command: "go"}, wantRule: "default-deny:" + string(KindShell)},
		{req: Request{Kind: KindGit, Subcommand: "status"}, wantRule: "default-deny:" + string(KindGit)},
	}
	for _, tc := range cases {
		t.Run(tc.wantRule, func(t *testing.T) {
			d := eng.Check(tc.req)
			if d.Allowed {
				t.Errorf("Check(%+v) allowed with empty policy; want deny", tc.req)
			}
			if d.Rule != tc.wantRule {
				t.Errorf("Rule = %q, want %q", d.Rule, tc.wantRule)
			}
		})
	}
}

func TestFSMatrix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755); err != nil {
		t.Fatalf("mkdir src/deep: %v", err)
	}

	eng, err := New(PermissionsPolicy{
		FS: FSPermissions{
			Read:  []string{"./**"},
			Write: []string{"src/**", "docs/README.md", "out/", "*.log"},
		},
	}, root, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inWS := func(rel ...string) string { return filepath.Join(append([]string{root}, rel...)...) }

	cases := []struct {
		name      string
		kind      Kind
		path      string
		wantAllow bool
		wantRule  string
	}{
		{
			name:      "read all hits doublestar nested",
			kind:      KindFsRead,
			path:      inWS("src", "deep", "f.txt"),
			wantAllow: true,
			wantRule:  "fs.read:./**",
		},
		{
			name:      "write hits direct child of src/**",
			kind:      KindFsWrite,
			path:      inWS("src", "main.go"),
			wantAllow: true,
			wantRule:  "fs.write:src/**",
		},
		{
			name:      "single star does not cross directories",
			kind:      KindFsWrite,
			path:      inWS("sub", "t.log"),
			wantAllow: false,
			wantRule:  "default-deny:" + string(KindFsWrite),
		},
		{
			name:      "exact file write hit",
			kind:      KindFsWrite,
			path:      inWS("docs", "README.md"),
			wantAllow: true,
			wantRule:  "fs.write:docs/README.md",
		},
		{
			name:      "trailing slash pattern covers dir contents",
			kind:      KindFsWrite,
			path:      inWS("out", "bin.o"),
			wantAllow: true,
			wantRule:  "fs.write:out/",
		},
		{
			name:      "trailing slash does not cover the dir itself",
			kind:      KindFsWrite,
			path:      inWS("out"),
			wantAllow: false,
			wantRule:  "default-deny:" + string(KindFsWrite),
		},
		{
			name:      "read list does not grant write",
			kind:      KindFsRead,
			path:      inWS("docs", "README.md"),
			wantAllow: true,
			wantRule:  "fs.read:./**",
		},
		{
			name:      "escape attempt auto-denied",
			kind:      KindFsRead,
			path:      filepath.Join(base, "secrets.txt"),
			wantAllow: false,
			wantRule:  "default-deny:" + string(KindFsRead),
		},
		{
			name:      "dotdot escape attempt auto-denied",
			kind:      KindFsRead,
			path:      inWS("src", "..", "..", "escape.txt"),
			wantAllow: false,
			wantRule:  "default-deny:" + string(KindFsRead),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(Request{Kind: tc.kind, Path: tc.path})
			if d.Allowed != tc.wantAllow {
				t.Errorf("Check(path=%s) allowed=%v, want %v (rule %q)", tc.path, d.Allowed, tc.wantAllow, d.Rule)
			}
			if d.Rule != tc.wantRule {
				t.Errorf("Rule = %q, want %q", d.Rule, tc.wantRule)
			}
		})
	}

	t.Run("relative request paths resolve against cwd workspace", func(t *testing.T) {
		t.Chdir(root)
		if d := eng.Check(Request{Kind: KindFsRead, Path: "src/deep/f.txt"}); !d.Allowed {
			t.Errorf("relative in-workspace read denied: %+v", d)
		}
		d := eng.Check(Request{Kind: KindFsRead, Path: "../outside.txt"})
		if d.Allowed || d.Rule != "default-deny:"+string(KindFsRead) {
			t.Errorf("relative escape read got %+v, want default-deny", d)
		}
	})

	t.Run("symlink target is not resolved", func(t *testing.T) {
		outside := filepath.Join(base, "outside-secret.txt")
		if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
			t.Fatalf("write outside file: %v", err)
		}
		link := filepath.Join(root, "link.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable here (%v); skipping", err)
		}
		// The request is judged by the path it claims: link.txt sits inside
		// the workspace lexically, so it matches "./**". Whether the TARGET
		// is readable is the OS isolation layer's concern (RNF-4.7).
		d := eng.Check(Request{Kind: KindFsRead, Path: link})
		if !d.Allowed || d.Rule != "fs.read:./**" {
			t.Errorf("symlinked path judged other than by its given form: %+v", d)
		}
	})
}

func TestEscapeRescuedByAbsolutePattern(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	secretsDir := filepath.Join(base, "secrets")
	vault := filepath.Join(secretsDir, "vault.txt")

	absolutePattern := filepath.ToSlash(secretsDir) + "/**"

	withoutRescue, err := New(PermissionsPolicy{
		FS: FSPermissions{Read: []string{"./**"}},
	}, root, nil)
	if err != nil {
		t.Fatalf("New without rescue: %v", err)
	}
	withRescue, err := New(PermissionsPolicy{
		FS: FSPermissions{Read: []string{"./**", absolutePattern}},
	}, root, nil)
	if err != nil {
		t.Fatalf("New with rescue: %v", err)
	}

	if d := withoutRescue.Check(Request{Kind: KindFsRead, Path: vault}); d.Allowed {
		t.Errorf("escaped path allowed without absolute pattern: %+v", d)
	}
	d := withRescue.Check(Request{Kind: KindFsRead, Path: vault})
	if !d.Allowed || d.Rule != "fs.read:"+absolutePattern {
		t.Errorf("escaped path not rescued by absolute pattern: %+v (want rule %q)", d, "fs.read:"+absolutePattern)
	}
}

func TestShellMatching(t *testing.T) {
	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Shell.Allow = []string{"go"}
	})

	cases := []struct {
		name      string
		command   string
		args      []string
		wantAllow bool
		wantRule  string
	}{
		{name: "bare name hit", command: "go", wantAllow: true, wantRule: "shell.exec:go"},
		{name: "posix pathed basename hit", command: "/usr/local/bin/go", wantAllow: true, wantRule: "shell.exec:go"},
		{name: "windows pathed basename hit", command: `C:\Tools\bin\go`, wantAllow: true, wantRule: "shell.exec:go"},
		{name: "case-insensitive match", command: "GO", wantAllow: true, wantRule: "shell.exec:go"},
		{name: "prefix names do not match", command: "gofmt", wantAllow: false, wantRule: "default-deny:" + string(KindShell)},
		{name: "miss denied", command: "rm", wantAllow: false, wantRule: "default-deny:" + string(KindShell)},
		{name: "args irrelevant to decision", command: "go", args: []string{"test", "./...", "-run", "TestX"}, wantAllow: true, wantRule: "shell.exec:go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(Request{Kind: KindShell, Command: tc.command, Args: tc.args})
			if d.Allowed != tc.wantAllow || d.Rule != tc.wantRule {
				t.Errorf("Check(command=%q) = %+v, want allowed=%v rule=%q", tc.command, d, tc.wantAllow, tc.wantRule)
			}
		})
	}

	t.Run("empty allow denies everything", func(t *testing.T) {
		locked, _ := newTestEngine(t, func(p *PermissionsPolicy) { p.Shell.Allow = []string{} })
		if d := locked.Check(Request{Kind: KindShell, Command: "go"}); d.Allowed {
			t.Errorf("empty allowlist allowed execution: %+v", d)
		}
	})
}

func TestGitAllowlistViaEngine(t *testing.T) {
	allowedSubcommands := []string{"status", "add", "commit", "log", "diff", "branch", "switch", "stash", "restore", "show", "remote", "fetch"}
	eng, _ := newTestEngine(t, nil)

	for _, sub := range allowedSubcommands {
		t.Run("allows "+sub, func(t *testing.T) {
			d := eng.Check(Request{Kind: KindGit, Subcommand: sub})
			if !d.Allowed || d.Rule != "git:"+sub {
				t.Errorf("Check(%s) = %+v, want allowed with rule %q", sub, d, "git:"+sub)
			}
		})
	}

	cases := []struct {
		name     string
		sub      string
		gitArgs  []string
		wantRule string
	}{
		{name: "unlisted subcommand denied", sub: "rebase", wantRule: "default-deny:" + string(KindGit)},
		{name: "uppercase convention rejected", sub: "COMMIT", wantRule: "default-deny:" + string(KindGit)},
		{name: "mixed case rejected", sub: "Status", wantRule: "default-deny:" + string(KindGit)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(Request{Kind: KindGit, Subcommand: tc.sub, GitArgs: tc.gitArgs})
			if d.Allowed || d.Rule != tc.wantRule {
				t.Errorf("Check(%s) = %+v, want deny with %q", tc.sub, d, tc.wantRule)
			}
		})
	}
}

func TestFloorBeatsAllowlist(t *testing.T) {
	// Every floored subcommand is ALSO in the allowlist: only the floor can
	// deny these, proving precedence.
	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Git.Allow = []string{"push", "reset", "clean", "branch"}
	})

	cases := []struct {
		name    string
		sub     string
		gitArgs []string
	}{
		{name: "push --force", sub: "push", gitArgs: []string{"--force", "origin"}},
		{name: "push -f", sub: "push", gitArgs: []string{"-f"}},
		{name: "push -ff cluster", sub: "push", gitArgs: []string{"-ff"}},
		{name: "push --FORCE case-insensitive", sub: "push", gitArgs: []string{"--FORCE"}},
		{name: "reset --hard first position", sub: "reset", gitArgs: []string{"--hard"}},
		{name: "reset --hard mid position", sub: "reset", gitArgs: []string{"HEAD~3", "--hard", "--quiet"}},
		{name: "clean bare", sub: "clean"},
		{name: "clean dry-run", sub: "clean", gitArgs: []string{"-n"}},
		{name: "clean force dirs", sub: "clean", gitArgs: []string{"-fd"}},
		{name: "branch -D", sub: "branch", gitArgs: []string{"-D", "feature"}},
		{name: "branch --delete", sub: "branch", gitArgs: []string{"--delete", "feature"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(Request{Kind: KindGit, Subcommand: tc.sub, GitArgs: tc.gitArgs})
			if d.Allowed {
				t.Fatalf("floor failed to deny %s %v: %+v", tc.sub, tc.gitArgs, d)
			}
			if d.Rule != "git-floor" {
				t.Errorf("Rule = %q, want \"git-floor\"", d.Rule)
			}
		})
	}

	nonFloor := []struct {
		name    string
		sub     string
		gitArgs []string
	}{
		{name: "force-with-lease stays allowable", sub: "push", gitArgs: []string{"--force-with-lease", "origin", "main"}},
		{name: "reset soft allowed", sub: "reset", gitArgs: []string{"--soft", "HEAD~1"}},
		{name: "branch lowercase delete allowed", sub: "branch", gitArgs: []string{"-d", "merged"}},
		{name: "plain branch allowed", sub: "branch", gitArgs: []string{"--list"}},
	}
	for _, tc := range nonFloor {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(Request{Kind: KindGit, Subcommand: tc.sub, GitArgs: tc.gitArgs})
			if !d.Allowed {
				t.Errorf("non-floor invocation denied: %s %v -> %+v", tc.sub, tc.gitArgs, d)
			}
		})
	}
}

func TestCustomFloor(t *testing.T) {
	// Mirror of TestFloorBeatsAllowlist for the allow-side floor: with a
	// deny rule present, only the rule can block the tool; everything else
	// falls through to the floor, proving both directions of precedence.
	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Custom.Deny = []string{"compaction_summarize"}
	})

	cases := []struct {
		name      string
		req       Request
		wantAllow bool
		wantRule  string
	}{
		{
			name:      "internal tool allowed by floor",
			req:       Request{Kind: KindCustom, Command: "retrieval_search"},
			wantAllow: true,
			wantRule:  "floor:custom",
		},
		{
			name:      "anchoring tool allowed by floor",
			req:       Request{Kind: KindCustom, Command: "anchoring_list"},
			wantAllow: true,
			wantRule:  "floor:custom",
		},
		{
			name:      "explicit deny rule takes precedence over floor",
			req:       Request{Kind: KindCustom, Command: "compaction_summarize"},
			wantAllow: false,
			wantRule:  "custom:compaction_summarize",
		},
		{
			name:      "empty tool name is malformed",
			req:       Request{Kind: KindCustom},
			wantAllow: false,
			wantRule:  "malformed-request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eng.Check(tc.req)
			if d.Allowed != tc.wantAllow {
				t.Errorf("Check(%+v) allowed = %v; want %v", tc.req, d.Allowed, tc.wantAllow)
			}
			if d.Rule != tc.wantRule {
				t.Errorf("Rule = %q, want %q", d.Rule, tc.wantRule)
			}
		})
	}

	t.Run("empty policy leaves floor allowing everything", func(t *testing.T) {
		// Every engine constructed elsewhere (daemon, e2e harness) leaves
		// Custom at its zero value: the floor must allow through it.
		open, _ := newTestEngine(t, nil)
		for _, tool := range []string{"retrieval_search", "compaction_summarize", "anchoring_get"} {
			if d := open.Check(Request{Kind: KindCustom, Command: tool}); !d.Allowed || d.Rule != "floor:custom" {
				t.Errorf("Check(%s) = %+v, want allowed with rule %q", tool, d, "floor:custom")
			}
		}
	})
}

func TestEngineConcurrentChecksAreStable(t *testing.T) {
	eng, root := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Shell.Allow = []string{"go"}
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	goroutines := 8
	iterations := 50
	if runtime.GOOS == "js" || runtime.GOOS == "wasip1" {
		goroutines, iterations = 2, 5
	}
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if d := eng.Check(Request{Kind: KindShell, Command: "go"}); !d.Allowed || d.Rule != "shell.exec:go" {
					errCh <- fmt.Errorf("shell decision drifted: %+v", d)
					return
				}
				if d := eng.Check(Request{Kind: KindGit, Subcommand: "push", GitArgs: []string{"--force"}}); d.Allowed || d.Rule != "git-floor" {
					errCh <- fmt.Errorf("floor decision drifted: %+v", d)
					return
				}
				if d := eng.Check(Request{Kind: KindFsRead, Path: filepath.Join(root, "x.txt")}); !d.Allowed {
					errCh <- fmt.Errorf("fs decision drifted: %+v", d)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}
