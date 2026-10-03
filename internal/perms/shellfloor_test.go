package perms

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestShellFloor(t *testing.T) {
	eng, root := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Shell.Allow = []string{"go", "git", "./scripts/check.sh", "npm run *"}
	})

	cases := []struct {
		name      string
		req       Request
		wantAllow bool
		wantRule  string
		// windowsOnly: "\" is a path separator only on Windows.
		windowsOnly bool
	}{
		// Path-qualified commands inside the workspace (agent-writable).
		{name: "workspace-relative program shadowing an allowed name", req: Request{Kind: KindShell, Command: "./src/go"}, wantRule: "shell-workspace-executable"},
		{name: "windows-style workspace-relative program", req: Request{Kind: KindShell, Command: `src\go`}, wantRule: "shell-workspace-executable", windowsOnly: true},
		{name: "absolute path inside workspace", req: Request{Kind: KindShell, Command: filepath.Join(root, "bin", "go")}, wantRule: "shell-workspace-executable"},
		{name: "exactly allowlisted workspace script", req: Request{Kind: KindShell, Command: "./scripts/check.sh"}, wantAllow: true, wantRule: "shell.exec:./scripts/check.sh"},
		{name: "allowlisted script without dot prefix", req: Request{Kind: KindShell, Command: `scripts\check.sh`}, wantAllow: true, wantRule: "shell.exec:./scripts/check.sh", windowsOnly: true},

		// git through the shell honors the git floor.
		{name: "shell git force push", req: Request{Kind: KindShell, Command: "git", Args: []string{"push", "--force"}}, wantRule: "git-floor"},
		{name: "shell git with global option before reset --hard", req: Request{Kind: KindShell, Command: "git", Args: []string{"-C", "sub", "reset", "--hard"}}, wantRule: "git-floor"},
		{name: "shell git.exe clean", req: Request{Kind: KindShell, Command: "GIT.EXE", Args: []string{"clean", "-n"}}, wantRule: "git-floor"},
		{name: "shell git status still allowed", req: Request{Kind: KindShell, Command: "git", Args: []string{"status"}}, wantAllow: true, wantRule: "shell.exec:git"},

		// Working directory confinement.
		{name: "shell workdir outside workspace", req: Request{Kind: KindShell, Command: "go", Workdir: filepath.Dir(root)}, wantRule: "workdir-outside-workspace"},
		{name: "shell workdir escaping via dotdot", req: Request{Kind: KindShell, Command: "go", Workdir: "../.."}, wantRule: "workdir-outside-workspace"},
		{name: "shell workdir inside workspace", req: Request{Kind: KindShell, Command: "go", Workdir: "internal"}, wantAllow: true, wantRule: "shell.exec:go"},
		{name: "git tool workdir outside workspace", req: Request{Kind: KindGit, Subcommand: "status", Workdir: filepath.Dir(root)}, wantRule: "workdir-outside-workspace"},

		// Argument patterns.
		{name: "arg pattern match", req: Request{Kind: KindShell, Command: "npm", Args: []string{"run", "lint"}}, wantAllow: true, wantRule: "shell.exec:npm run *"},
		{name: "arg pattern miss", req: Request{Kind: KindShell, Command: "npm", Args: []string{"install", "evil"}}, wantRule: "default-deny:" + string(KindShell)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.windowsOnly && runtime.GOOS != "windows" {
				t.Skip(`"\" is a path separator only on Windows`)
			}
			d := eng.Check(tc.req)
			if d.Allowed != tc.wantAllow || d.Rule != tc.wantRule {
				t.Errorf("Check(%+v) = %+v, want allowed=%v rule=%q", tc.req, d, tc.wantAllow, tc.wantRule)
			}
		})
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"test *", "test ./... -run X", true},
		{"test *", "build ./...", false},
		{"run lint", "run lint", true},
		{"run lint", "run lint --fix", false},
		{"*", "", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*/x/*", "a/x/b", true},
	}
	for _, tc := range cases {
		if got := wildcardMatch(tc.pattern, tc.s); got != tc.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// CI regression (ubuntu/macos, 2026-10-03): the shell floor treated "\" as
// a path separator everywhere. On Unix it is an ordinary filename character:
// a command like `C:\Tools\bin\go` has no "/", so exec looks it up in PATH,
// never in the workspace — the floor must not apply to it there.
func TestShellFloorUsesPlatformSeparators(t *testing.T) {
	orig := programPathSeps
	defer func() { programPathSeps = orig }()
	programPathSeps = "/" // as on Linux/macOS

	eng, _ := newTestEngine(t, func(p *PermissionsPolicy) { p.Shell.Allow = []string{"go"} })
	if d := eng.Check(Request{Kind: KindShell, Command: `C:\Tools\bin\go`}); !d.Allowed {
		t.Fatalf("unix: a backslash-only command is a PATH lookup, not a workspace file: %+v", d)
	}
	if d := eng.Check(Request{Kind: KindShell, Command: "./src/go"}); d.Allowed || d.Rule != "shell-workspace-executable" {
		t.Fatalf("unix: ./src/go is still a workspace executable: %+v", d)
	}
}
