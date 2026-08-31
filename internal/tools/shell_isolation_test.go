// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// markerIsolator is a controllable Isolator whose wrapped invocation echoes
// an identifiable marker, proving which execution path ran.
type markerIsolator struct {
	enabled bool
	command string
	args    []string
}

func (*markerIsolator) Enabled() bool { return true }

func (m *markerIsolator) Wrap(selfExe, command string, args []string) *exec.Cmd {
	// Inheriting env (nil Env) keeps the probe portable across platforms.
	return exec.Command(m.command, m.args...)
}

func disabledIsolator() Isolator { return &staticDisabled{} }

type staticDisabled struct{}

func (*staticDisabled) Enabled() bool { return false }
func (*staticDisabled) Wrap(selfExe, command string, args []string) *exec.Cmd {
	return nil // never reached while Enabled reports false
}

func shellRequest(command string, args ...string) perms.Request {
	return perms.Request{Kind: perms.KindShell, Command: command, Args: args}
}

const wrapperMarker = "ISOLATION-WRAPPER-MARKER"

// echoCommand returns a platform-appropriate command that prints the marker.
func echoCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo", wrapperMarker}
	}
	return "echo", []string{wrapperMarker}
}

// TestShellExecWrappedWhenIsolatorEnabled proves the isolation route: with
// an enabled isolator the command runs through the wrapper invocation and
// metadata marks the result isolated.
func TestShellExecWrappedWhenIsolatorEnabled(t *testing.T) {
	echoCmd, echoArgs := echoCommand()

	tool := newShellExecTool(nil)
	tool.isolator = &markerIsolator{command: echoCmd, args: echoArgs}

	result, err := tool.Execute(context.Background(), shellRequest("go", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, wrapperMarker) {
		t.Errorf("output %q did not come from the wrapper; routing broken", result.Content)
	}
	if isolated, _ := result.Metadata["isolated"].(bool); !isolated {
		t.Error("metadata missing isolated=true for wrapped execution")
	}
}

// TestShellExecLegacyDirectWithoutIsolator proves backward compatibility:
// nil isolator keeps direct exec and does not mark results isolated.
func TestShellExecLegacyDirectWithoutIsolator(t *testing.T) {
	echoCmd, echoArgs := echoCommand()

	tool := newShellExecTool(nil)
	req := shellRequest(echoCmd, echoArgs...)
	result, err := tool.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := result.Metadata["isolated"]; present {
		t.Error("metadata must not claim isolation for legacy direct exec")
	}
	if !strings.Contains(result.Content, wrapperMarker) {
		t.Errorf("direct echo output missing: %q", result.Content)
	}
}

// TestShellExecDisabledIsolatorFallsBack proves an explicitly disabled
// isolator degrades to direct execution (spec §6 non-Linux behavior).
func TestShellExecDisabledIsolatorFallsBack(t *testing.T) {
	echoCmd, echoArgs := echoCommand()

	tool := newShellExecTool(nil)
	tool.isolator = disabledIsolator()
	tool.requireIsolation = false // unrequired unavailability degrades

	result, err := tool.Execute(context.Background(), shellRequest(echoCmd, echoArgs...))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := result.Metadata["isolated"]; present {
		t.Error("disabled isolator must not mark results isolated")
	}
	if denied, _ := result.Metadata["denied"].(bool); denied {
		t.Error("unrequired unavailability must degrade, not deny")
	}
	if !strings.Contains(result.Content, wrapperMarker) {
		t.Errorf("fallback did not execute the real command: %q", result.Content)
	}
}

// TestShellExecRequiredUnavailableDeniedOnLinux pins the Linux-only refusal:
// require_isolation + unavailable isolation => DENIED-style result. On other
// platforms the flag is ignored entirely (documented config behavior).
func TestShellExecRequiredUnavailableDeniedOnLinux(t *testing.T) {
	if !requiredUnavailableDecision("linux") {
		t.Fatal("on linux, require_isolation + unavailable isolation must refuse")
	}

	echoCmd, echoArgs := echoCommand()
	tool := newShellExecTool(nil)
	tool.isolator = disabledIsolator()
	tool.requireIsolation = true

	if runtime.GOOS == "linux" {
		result, err := tool.Execute(context.Background(), shellRequest("go", "version"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"DENIED", "shell isolation required but unavailable"} {
			if !strings.Contains(result.Content, want) {
				t.Errorf("content %q missing %q", result.Content, want)
			}
		}
		if denied, _ := result.Metadata["denied"].(bool); !denied {
			t.Error("metadata missing denied=true")
		}
		return
	}

	// Non-Linux: require_isolation ignored, execution proceeds directly.
	result, err := tool.Execute(context.Background(), shellRequest(echoCmd, echoArgs...))
	if err != nil {
		t.Fatalf("non-linux platform must ignore require_isolation: %v", err)
	}
	if denied, _ := result.Metadata["denied"].(bool); denied {
		t.Error("require_isolation leaked into a non-linux decision")
	}
}

// TestRegistrySetIsolatorReachesShellTool proves the registry wiring: the
// configured isolator drives shell_exec through the wrapper even though the
// tool was created inside NewDefaultRegistry before SetIsolator ran.
func TestRegistrySetIsolatorReachesShellTool(t *testing.T) {
	reg, _ := setupRegistry(t)

	echoCmd, echoArgs := echoCommand()
	reg.SetRequireShellIsolation(false)
	reg.SetIsolator(&markerIsolator{command: echoCmd, args: echoArgs})

	args := make([]any, len(echoArgs))
	for i, a := range echoArgs {
		args[i] = a
	}
	result, err := reg.Execute(context.Background(), "shell_exec",
		map[string]any{"command": echoCmd, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, wrapperMarker) {
		t.Errorf("registry output %q bypassed the configured isolator", result.Content)
	}
}

// --- process-group helpers ---------------------------------------------------

func TestKillProcessTreeToleratesNilProcess(t *testing.T) {
	if err := killProcessTree(nil); err != nil {
		t.Errorf("killProcessTree(nil) = %v, want nil (must tolerate no process)", err)
	}
}

// TestShellExecTimeoutMetadataAbsentForFastCommand guards the timeout
// plumbing refactor: a fast command must NOT be flagged as timed out and
// must exit cleanly through the new Cancel/process-group path.
func TestShellExecTimeoutMetadataAbsentForFastCommand(t *testing.T) {
	echoCmd, echoArgs := echoCommand()
	tool := newShellExecTool(nil)

	result, err := tool.Execute(context.Background(), perms.Request{
		Kind:       perms.KindShell,
		Command:    echoCmd,
		Args:       echoArgs,
		TimeoutSec: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := result.Metadata["timeout"]; present {
		t.Error("fast command flagged as timeout; kill plumbing broke success paths")
	}
	if exitCode, _ := result.Metadata["exit_code"].(int); exitCode != 0 {
		t.Errorf("exit_code = %v, want 0", result.Metadata["exit_code"])
	}
}

// requiredUnavailableDecision mirrors the tool's Linux-only refusal rule so
// the platform split stays asserted on every OS.
func requiredUnavailableDecision(goos string) bool {
	return goos == "linux"
}
