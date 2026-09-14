package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSpecPath(t *testing.T) {
	// On case-insensitive filesystems (Windows/macOS) the probe returns the
	// first candidate name that exists under any casing.
	wantLower := "spec.md"
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		wantLower = "SPEC.md"
	}
	table := []struct {
		name           string
		flagPath       string
		cfgSpecPath    string
		createProbe    string
		want           string
		wantErrMessage string
	}{
		{name: "flag wins over config and probe", flagPath: "docs/my-spec.md", cfgSpecPath: "SPEC.md", createProbe: "spec-harness-agentic.md", want: "docs/my-spec.md"},
		{name: "flag wins when probing finds nothing", flagPath: "x.md", want: "x.md"},
		{name: "config wins over probe", cfgSpecPath: "docs/spec.md", createProbe: "SPEC.md", want: "docs/spec.md"},
		{name: "config used even when probing finds nothing", cfgSpecPath: "custom.md", want: "custom.md"},
		{name: "probe hits canonical name", createProbe: "spec-harness-agentic.md", want: "spec-harness-agentic.md"},
		{name: "probe hits uppercase fallback", createProbe: "SPEC.md", want: "SPEC.md"},
		{name: "probe hits lowercase fallback", createProbe: "spec.md", want: wantLower},
		{name: "nothing found lists probed names", wantErrMessage: "spec file not found"},
		{name: "blank flags fall back to probe", flagPath: "  ", cfgSpecPath: "  ", createProbe: "spec.md", want: wantLower},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.createProbe != "" {
				if err := os.WriteFile(filepath.Join(root, tc.createProbe), []byte("spec"), 0o644); err != nil {
					t.Fatalf("write probe: %v", err)
				}
			}
			got, err := ResolveSpecPath(root, tc.flagPath, tc.cfgSpecPath)
			if tc.wantErrMessage != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMessage) {
					t.Fatalf("ResolveSpecPath err = %v, want containing %q", err, tc.wantErrMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSpecPath unexpected err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveSpecPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpecCommandConstruction(t *testing.T) {
	cmd := newSpecCommand()
	if cmd.Use != "spec" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, name := range []string{"show", "log", "diff"} {
		if _, _, err := cmd.Find([]string{name}); err != nil {
			t.Fatalf("spec must expose %s subcommand: %v", name, err)
		}
	}
	diff := newSpecDiffCommand()
	if f := diff.Flags().Lookup("json"); f == nil {
		t.Fatal("spec diff must expose --json")
	}
	if f := diff.Flags().Lookup("path"); f == nil {
		t.Fatal("spec diff must expose --path")
	}
	if err := diff.Args(diff, nil); err == nil {
		t.Fatal("spec diff requires at least one ref")
	}
	if err := diff.Args(diff, []string{"a", "b", "c"}); err == nil {
		t.Fatal("spec diff takes at most two refs")
	}
}

func TestSpecJSONEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("external git flow")
	}
	root := initSpecFixture(t)
	out := execSpecCommand(t, root, []string{"--json"})
	var env struct {
		OK      bool   `json:"ok"`
		Command string `json:"command"`
		Result  struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Command != "spec show" {
		t.Fatalf("envelope = ok:%v command:%q", env.OK, env.Command)
	}
	if !strings.Contains(env.Result.Content, specContentHead) {
		t.Fatalf("envelope content missing seeded text: %q", env.Result.Content)
	}
	if env.Result.Path != "SPEC.md" {
		t.Fatalf("envelope path = %q, want SPEC.md", env.Result.Path)
	}
}

// Integration fixture: a scratch git repo with a spec committed twice.
func initSpecFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("external git flow")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "SPEC.md"), []byte(specContentHead), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	git("add", "SPEC.md")
	git("commit", "-m", "spec: initial")
	if err := os.WriteFile(filepath.Join(root, "SPEC.md"), []byte(specContentHead+"\n"+specContentTail), 0o644); err != nil {
		t.Fatalf("write spec v2: %v", err)
	}
	git("add", "SPEC.md")
	git("commit", "-m", "spec: improve")
	if err := os.WriteFile(filepath.Join(root, "untracked.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}
	return root
}

const specContentHead = "# Harness Spec\n\nversion 1"
const specContentTail = "version 2 addition"

// execSpecCommand runs the "spec" subcommand against root in-process with the
// given arguments and returns the captured stdout buffer.
func execSpecCommand(t *testing.T, root string, args []string) []byte {
	t.Helper()
	t.Chdir(root)
	buf := &bytes.Buffer{}
	cmd := newSpecShowCommand()
	cmd.SetArgs(args)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("spec show %v: %v", args, err)
	}
	return buf.Bytes()
}

func TestSpecGitIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("external git flow")
	}
	root := initSpecFixture(t)
	t.Run("log returns two entries", func(t *testing.T) {
		entries, err := runSpecFileLog(root, "SPEC.md")
		if err != nil {
			t.Fatalf("runSpecFileLog: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(entries))
		}
		if entries[0].Subject != "spec: improve" || entries[1].Subject != "spec: initial" {
			t.Fatalf("subjects = %q, %q", entries[0].Subject, entries[1].Subject)
		}
		if entries[0].Hash == "" || entries[0].ShortHash == "" || entries[0].Author == "" || entries[0].Date == "" {
			t.Fatalf("first entry fields incomplete: %+v", entries[0])
		}
	})

	t.Run("show prints content", func(t *testing.T) {
		content, err := runSpecFileShow(root, "SPEC.md")
		if err != nil {
			t.Fatalf("runSpecFileShow: %v", err)
		}
		if !strings.Contains(content, specContentTail) {
			t.Fatalf("show content missing v2 text: %q", content)
		}
	})

	t.Run("diff between refs shows change", func(t *testing.T) {
		diff, err := runSpecFileDiff(root, "SPEC.md", "HEAD~1", "HEAD")
		if err != nil {
			t.Fatalf("runSpecFileDiff: %v", err)
		}
		if !strings.Contains(diff, specContentTail) {
			t.Fatalf("diff missing added line: %q", diff)
		}
	})

	t.Run("single ref diffs working tree", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "SPEC.md"), []byte(specContentHead+"\nworking-tree edit"), 0o644); err != nil {
			t.Fatalf("write working tree: %v", err)
		}
		diff, err := runSpecFileDiff(root, "SPEC.md", "HEAD", "")
		if err != nil {
			t.Fatalf("runSpecFileDiff: %v", err)
		}
		if !strings.Contains(diff, "working-tree edit") {
			t.Fatalf("diff missing working tree edit: %q", diff)
		}
	})
}

func TestSpecGitCleanErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("external git flow")
	}
	t.Run("outside git repo", func(t *testing.T) {
		root := t.TempDir()
		_, err := runSpecFileLog(root, "SPEC.md")
		if err == nil {
			t.Fatal("want error outside a git repository")
		}
		if got := err.Error(); !strings.Contains(got, "not a git repository") {
			t.Fatalf("error = %q, want \"not a git repository\" detail", got)
		}
	})

	t.Run("invalid ref surfaces git stderr", func(t *testing.T) {
		root := initSpecFixture(t)
		_, err := runSpecFileDiff(root, "SPEC.md", "no-such-ref", "HEAD")
		if err == nil {
			t.Fatal("want error for missing ref")
		}
		if !strings.Contains(err.Error(), "no-such-ref") {
			t.Fatalf("error should carry the bad ref detail: %v", err)
		}
	})
}

func TestRunSpecLogMissingSpecErrors(t *testing.T) {
	// Resolution failure inside the run wrappers must surface without a git
	// call: no context App -> empty config, no probe -> resolution error.
	cmd := newSpecLogCommand()
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)
	err := runSpecLog(cmd, &buf, "", false)
	if err == nil {
		t.Fatal("want resolution error when no spec file exists")
	}
	if !strings.Contains(err.Error(), "spec file not found") {
		t.Fatalf("err = %v, want missing-spec resolution error", err)
	}
}
