package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Models often send the whole command line as "command" ("go version").
// That never matched a "go version" rule (program "go" + args "version")
// and fell to default-deny, and exec would have looked for a program
// literally named "go version". The request now fails with an actionable
// error the model can correct on its next call.
func TestShellExecRejectsCommandLineInCommand(t *testing.T) {
	_, err := BuildPermsRequest("shell_exec", map[string]any{"command": "go version"})
	if err == nil {
		t.Fatal("want an error for a command line in \"command\"")
	}
	for _, want := range []string{`"args"`, `"command": "go"`, `["version"]`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should show the fix (missing %s)", err, want)
		}
	}
}

func TestShellExecAcceptsSplitCommand(t *testing.T) {
	req, err := BuildPermsRequest("shell_exec", map[string]any{"command": "go", "args": []any{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Command != "go" || len(req.Args) != 1 || req.Args[0] != "version" {
		t.Fatalf("req = %+v", req)
	}
}

// An existing executable whose path contains spaces is legitimate.
func TestShellExecAcceptsExecutablePathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Program Files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPermsRequest("shell_exec", map[string]any{"command": exe}); err != nil {
		t.Fatalf("existing path with spaces rejected: %v", err)
	}
}
