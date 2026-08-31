package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/client"
)

// execRoot runs the real root command with the given arguments, with the
// home directory redirected so ~/.forge/daemon.addr cannot leak from the
// developer machine.
func execRoot(t *testing.T, args ...string) error {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	root := RootCommand // same instance Execute() runs and init() registers into
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	root.SetArgs(nil) // don't leak args into later invocations
	return err
}

func TestRunCommandMissingDaemonHintsForgeServe(t *testing.T) {
	err := execRoot(t, "run", "hello world")
	if err == nil {
		t.Fatal("expected failure without a running daemon")
	}
	if !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Fatalf("want ErrDaemonNotRunning chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "forge serve") {
		t.Errorf("error must point at 'forge serve', got %v", err)
	}
}

func TestChatCommandMissingDaemonHintsForgeServe(t *testing.T) {
	err := execRoot(t, "chat")
	if err == nil {
		t.Fatal("expected failure without a running daemon")
	}
	if !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Fatalf("want ErrDaemonNotRunning chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "forge serve") {
		t.Errorf("error must point at 'forge serve', got %v", err)
	}
}

func TestRunCommandBadFlagIsUsageError(t *testing.T) {
	err := execRoot(t, "run", "--nope", "hi")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("bad flag should map to UsageError (exit code 2), got %v", err)
	}
}

func TestRunCommandMissingPromptIsUsageError(t *testing.T) {
	err := execRoot(t, "run")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("missing prompt should map to UsageError (exit code 2), got %v", err)
	}
	if !strings.Contains(ue.Error(), "exactly 1") {
		t.Errorf("usage message should explain the arity rule, got %v", ue)
	}
}

func TestRunCommandEmptyPromptIsUsageError(t *testing.T) {
	err := execRoot(t, "run", "   ")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("empty prompt should map to UsageError, got %v", err)
	}
}

func TestRunCommandRegistersJSONAndSessionFlags(t *testing.T) {
	cmd := newRunCommand()
	for _, name := range []string{"json", "session"} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Errorf("run command missing --%s flag", name)
		}
	}
	if f := cmd.Flags().Lookup("json"); f != nil && f.DefValue != "false" {
		t.Errorf("--json default = %q, want false", f.DefValue)
	}
}

func TestChatCommandRegistersSessionFlag(t *testing.T) {
	cmd := newChatCommand()
	if f := cmd.Flags().Lookup("session"); f == nil {
		t.Error("chat command missing --session flag")
	}
}

func TestWriteHumanResultRoutesStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := oneShotFixture()
	writeHumanResult(&stdout, &stderr, res)

	if !strings.Contains(stdout.String(), "answer") {
		t.Errorf("answer must go to stdout, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "fs_read") {
		t.Errorf("tool trace leaked into stdout: %q", stdout.String())
	}
	for _, want := range []string{
		"-> fs_read(path=notes.txt)",
		"<- ok",
		"-> shell_exec(cmd=pwd)",
		"<- error",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr trace missing %q, got:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "answer\n") && !strings.HasPrefix(stderr.String(), "->") {
		t.Errorf("answer leaked into stderr: %q", stderr.String())
	}
}

func TestWriteJSONResultEmitsOnlyTheDocument(t *testing.T) {
	var out bytes.Buffer
	res := oneShotFixture()
	if err := writeJSONResult(&out, res); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	got := out.String()
	if !strings.HasPrefix(got, "{") {
		t.Errorf("output must be a bare JSON document, got %q", got)
	}
	for _, key := range []string{`"session_id": "os-1"`, `"model": "model-a"`, `"response": "answer"`, `"duration_ms": 12`} {
		if !strings.Contains(got, key) {
			t.Errorf("JSON output missing %s:\n%s", key, got)
		}
	}
	if !strings.Contains(got, `"name": "fs_read"`) || !strings.Contains(got, `"ok": true`) {
		t.Errorf("tool trace not serialized:\n%s", got)
	}
}

// oneShotFixture builds a representative result for writer tests.
func oneShotFixture() *client.OneShotResult {
	return &client.OneShotResult{
		SessionID: "os-1",
		Model:     "model-a",
		Response:  "answer",
		ToolCalls: []client.ToolCallTrace{
			{Name: "fs_read", Args: []byte(`{"path":"notes.txt"}`), OK: true},
			{Name: "shell_exec", Args: []byte(`{"cmd":"pwd"}`), OK: false},
		},
		DurationMs: 12,
	}
}
