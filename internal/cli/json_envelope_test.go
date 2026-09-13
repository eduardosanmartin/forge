package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/version"
)

// envelopeRaw is a decode helper that preserves Result as RawMessage for assertions.
type envelopeRaw struct {
	OK       bool            `json:"ok"`
	Command  string          `json:"command"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *string         `json:"error,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

func decodeRaw(t *testing.T, data []byte) envelopeRaw {
	t.Helper()
	var env envelopeRaw
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, string(data))
	}
	return env
}

func assertEnvelopeInvariants(t *testing.T, env envelopeRaw, wantCommand string) {
	t.Helper()
	if !env.OK {
		t.Fatalf("envelope ok=false, want true (command=%s)", wantCommand)
	}
	if env.Command != wantCommand {
		t.Errorf("command = %q, want %q", env.Command, wantCommand)
	}
	if env.Error != nil {
		t.Errorf("error should be nil on success, got %q", *env.Error)
	}
	if env.Result == nil || len(env.Result) == 0 {
		t.Errorf("result missing for command %q", wantCommand)
	}
	if env.Metadata == nil || env.Metadata["command"] != wantCommand {
		t.Errorf("metadata.command = %v, want %q, metadata=%v", env.Metadata["command"], wantCommand, env.Metadata)
	}
}

// --- run envelope (payload = OneShotResult) ---

func TestJSONEnvelope_Run(t *testing.T) {
	var out bytes.Buffer
	res := oneShotFixture()
	if err := writeJSONResult(&out, res); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "run")
	// result must contain run fields
	var result client.OneShotResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.SessionID != "os-1" || result.Model != "model-a" || result.Response != "answer" {
		t.Errorf("result mismatch: %+v", result)
	}
	if len(result.ToolCalls) != 2 {
		t.Errorf("tool calls = %d, want 2", len(result.ToolCalls))
	}
}

// --- version envelope ---

func TestJSONEnvelope_Version(t *testing.T) {
	var out bytes.Buffer
	cmd := newVersionCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("version --json: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "version")
	var vr versionResult
	if err := json.Unmarshal(env.Result, &vr); err != nil {
		t.Fatalf("unmarshal version result: %v", err)
	}
	if vr.Version != version.Version {
		t.Errorf("version = %q, want %q", vr.Version, version.Version)
	}
	if vr.Banner != version.String() {
		t.Errorf("banner = %q, want %q", vr.Banner, version.String())
	}
}

// --- status envelope (daemon not running => running:false) ---

func TestJSONEnvelope_Status_DaemonNotRunning(t *testing.T) {
	// Isolate HOME so no daemon.addr is found
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	var out bytes.Buffer
	cmd := newStatusCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "status")
	var sr daemon.StatusResult
	if err := json.Unmarshal(env.Result, &sr); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if sr.Running {
		t.Errorf("Running = true, want false when daemon not running")
	}
}

// --- sessions envelope shape (payload type check, no daemon required; we test writer helper directly) ---

func TestJSONEnvelope_Sessions_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.ListSessionsResult{
		Sessions: []daemon.SessionResult{
			{ID: "sess-1", MessageCount: 3, Metadata: map[string]any{"branch_parent": "abc"}},
		},
	}
	if err := writeJSONResultEnvelope(&out, "sessions", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "sessions")
	if !strings.Contains(string(env.Result), "sess-1") {
		t.Errorf("result missing sess-1: %s", string(env.Result))
	}
}

// --- session list envelope shape ---

func TestJSONEnvelope_SessionList_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.ListSessionsResult{
		Sessions: []daemon.SessionResult{{ID: "s-1", MessageCount: 1}},
	}
	if err := writeJSONResultEnvelope(&out, "session list", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "session list")
}

// --- plugin list envelope shape ---

func TestJSONEnvelope_PluginList_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.PluginListResult{
		Plugins: []daemon.PluginInfoResult{{Name: "demo", Version: "0.1.0", Source: "local", Enabled: true, ToolCount: 1}},
	}
	if err := writeJSONResultEnvelope(&out, "plugin list", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "plugin list")
	if !strings.Contains(string(env.Result), `"name": "demo"`) {
		t.Errorf("result missing demo: %s", string(env.Result))
	}
}

// --- skill list envelope shape ---

func TestJSONEnvelope_SkillList_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.SkillListResult{
		Skills: []daemon.SkillInfoResult{{Name: "my-skill", Description: "desc", Category: "custom", Source: "local", Enabled: true}},
	}
	if err := writeJSONResultEnvelope(&out, "skill list", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "skill list")
}

// --- error envelope shape ---

func TestJSONEnvelope_ErrorShape(t *testing.T) {
	var out bytes.Buffer
	if err := writeJSONErrorEnvelope(&out, "run", "boom"); err != nil {
		t.Fatalf("write: %v", err)
	}
	var env envelopeRaw
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK {
		t.Errorf("ok should be false on error")
	}
	if env.Command != "run" {
		t.Errorf("command = %q, want run", env.Command)
	}
	if env.Error == nil || *env.Error != "boom" {
		t.Errorf("error = %v, want boom", env.Error)
	}
	if env.Result != nil {
		t.Errorf("result should be absent on error, got %s", string(env.Result))
	}
}

// --- flag matrix: commands that must expose --json ---

func TestJSONFlagMatrix(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobraCommandLike
	}{
		// Use wrappers to avoid importing cobra type directly in table; we check via lookup.
	}
	_ = cases
	// Explicit checks for the stable matrix mandated by RF-6.3.
	mustHaveJSON := []struct {
		label string
		cmd   interface{ Flags() interface{ Lookup(string) interface{} } }
	}{
		// We check via concrete cobra.Command below.
	}
	_ = mustHaveJSON

	// Direct checks
	checks := map[string]bool{
		"run":          newRunCommand().Flags().Lookup("json") != nil,
		"version":      newVersionCommand().Flags().Lookup("json") != nil,
		"status":       newStatusCommand().Flags().Lookup("json") != nil,
		"sessions":     newSessionsCommand().Flags().Lookup("json") != nil,
		"session list": newSessionListCommand().Flags().Lookup("json") != nil,
		"plugin list":  newPluginListCommand().Flags().Lookup("json") != nil,
		"skill list":   newSkillListCommand().Flags().Lookup("json") != nil,
	}
	for label, ok := range checks {
		if !ok {
			t.Errorf("command %q must expose --json flag (RF-6.3 matrix)", label)
		}
	}
	// Interactive commands must NOT expose --json (human-only)
	interactive := map[string]bool{
		"serve":  newServeCommand().Flags().Lookup("json") == nil,
		"chat":   newChatCommand().Flags().Lookup("json") == nil,
		"tui":    newTUICommand().Flags().Lookup("json") == nil,
		"attach": newAttachCommand().Flags().Lookup("json") == nil,
	}
	for label, ok := range interactive {
		if !ok {
			t.Errorf("interactive command %q must NOT expose --json", label)
		}
	}
}

// cobraCommandLike is a tiny interface to avoid importing cobra in test table above.
type cobraCommandLike interface {
	Flags() interface{ Lookup(string) interface{} }
}
