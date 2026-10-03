package perms

import (
	"path/filepath"
	"testing"
)

func TestAskRules(t *testing.T) {
	eng, root := newTestEngine(t, func(p *PermissionsPolicy) {
		p.Shell.Allow = []string{"go"}
		p.Shell.Ask = []string{"npm", "git"}
		p.Git.Ask = []string{"push"}
		p.FS.Write = []string{"./src/**"}
		p.FS.AskWrite = []string{"./**"}
	})
	cases := []struct {
		name         string
		req          Request
		allowed, ask bool
		rule         string
	}{
		{"allow wins over ask", Request{Kind: KindShell, Command: "go"}, true, false, "shell.exec:go"},
		{"shell ask", Request{Kind: KindShell, Command: "npm", Args: []string{"install"}}, false, true, "ask:shell.exec:npm"},
		{"floor beats ask", Request{Kind: KindShell, Command: "git", Args: []string{"push", "--force"}}, false, false, "git-floor"},
		{"git ask", Request{Kind: KindGit, Subcommand: "push"}, false, true, "ask:git:push"},
		{"git floor beats ask", Request{Kind: KindGit, Subcommand: "push", GitArgs: []string{"-f"}}, false, false, "git-floor"},
		{"fs write allowed", Request{Kind: KindFsWrite, Path: filepath.Join(root, "src", "a.go")}, true, false, "fs.write:./src/**"},
		{"fs write ask", Request{Kind: KindFsWrite, Path: filepath.Join(root, "README.md")}, false, true, "ask:fs.write:./**"},
		{"unmatched stays plain deny", Request{Kind: KindShell, Command: "curl"}, false, false, "default-deny:shell.exec"},
	}
	for _, tc := range cases {
		d := eng.Check(tc.req)
		if d.Allowed != tc.allowed || d.Ask != tc.ask || d.Rule != tc.rule {
			t.Errorf("%s: got %+v, want allowed=%v ask=%v rule=%q", tc.name, d, tc.allowed, tc.ask, tc.rule)
		}
	}
}
