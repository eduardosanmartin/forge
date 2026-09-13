package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/client"
)

func decodeEnvelope(data []byte, env *JSONEnvelope) error {
	// Decode into a raw holder so Result stays as raw JSON for inspection.
	type rawEnv struct {
		OK       bool            `json:"ok"`
		Command  string          `json:"command"`
		Result   json.RawMessage `json:"result"`
		Error    *string         `json:"error"`
		Metadata map[string]any  `json:"metadata"`
	}
	var r rawEnv
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	env.OK = r.OK
	env.Command = r.Command
	env.Error = r.Error
	env.Metadata = r.Metadata
	if len(r.Result) > 0 {
		var v any
		if err := json.Unmarshal(r.Result, &v); err == nil {
			env.Result = v
		} else {
			env.Result = r.Result
		}
	}
	return nil
}

func marshalResult(v any) ([]byte, error) {
	return json.Marshal(v)
}

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
	if !strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("output must be a JSON document, got %q", got)
	}
	// New RF-6.3 envelope: ok, command, result, metadata at top-level;
	// the original OneShotResult fields live inside result.
	var env JSONEnvelope
	if err := decodeEnvelope([]byte(got), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, got)
	}
	if !env.OK {
		t.Fatalf("envelope ok = false, want true: %s", got)
	}
	if env.Command != "run" {
		t.Errorf("envelope command = %q, want run", env.Command)
	}
	if env.Metadata == nil || env.Metadata["command"] != "run" {
		t.Errorf("envelope metadata missing command=run: %+v", env.Metadata)
	}
	if env.Result == nil {
		t.Fatalf("envelope result missing: %s", got)
	}
	resultBytes, _ := marshalResult(env.Result)
	resultStr := string(resultBytes)
	for _, key := range []string{`"session_id":"os-1"`, `"model":"model-a"`, `"response":"answer"`, `"duration_ms":12`} {
		if !strings.Contains(resultStr, key) {
			t.Errorf("JSON result missing %s:\n%s", key, resultStr)
		}
	}
	if !strings.Contains(resultStr, `"name":"fs_read"`) || !strings.Contains(resultStr, `"ok":true`) {
		t.Errorf("tool trace not serialized:\n%s", resultStr)
	}
	if env.Error != nil {
		t.Errorf("envelope error should be nil on success, got %v", *env.Error)
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
