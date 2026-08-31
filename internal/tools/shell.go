// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// shellExecTool implements the shell_exec tool.
//
// Execution routing (spec RNF-4.7):
//   - With a non-nil, enabled Isolator: the command runs through forge's own
//     binary acting as an isolation wrapper child (Landlock + seccomp on
//     Linux), so OS-level containment bounds the user command while the
//     daemon itself stays unrestricted.
//   - Without isolation available: legacy direct exec, with a one-time
//     debug note that only the permissions model is enforcing (spec §6:
//     macOS v0 and other non-Linux platforms are permissions-only).
//   - On Linux with requireIsolation set but isolation unavailable: the
//     request is refused instead of silently degrading. Non-Linux platforms
//     ignore require_isolation entirely (documented config behavior).
type shellExecTool struct {
	logger           *slog.Logger
	isolator         Isolator
	requireIsolation bool
	legacyNoteOnce   sync.Once
}

func newShellExecTool(logger *slog.Logger) *shellExecTool {
	return &shellExecTool{logger: logger}
}

// setOptions injects registry-owned configuration into this tool instance.
func (t *shellExecTool) setOptions(logger *slog.Logger, isol Isolator, requireIsolation bool) {
	if logger != nil {
		t.logger = logger
	}
	t.isolator = isol
	t.requireIsolation = requireIsolation
}

func (t *shellExecTool) Name() string { return "shell_exec" }
func (t *shellExecTool) Description() string {
	return "Execute a shell command. Captures stdout+stderr combined, with timeout and output truncation."
}

func (t *shellExecTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Executable name or path to execute",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Command arguments",
			},
			"timeout_sec": map[string]any{
				"type":        "number",
				"description": "Timeout in seconds (default 120, max 300)",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for command execution. OMIT this field entirely to use the workspace root — that is almost always correct. Only set it to a directory path that appeared earlier in this conversation; never invent paths (e.g. /workspace).",
			},
		},
		"required": []string{"command"},
	}
}

func (t *shellExecTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	command := req.Command
	args := req.Args
	timeoutSec := req.TimeoutSec

	// Apply timeout bounds
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// --- Isolation routing (spec RNF-4.7) ---
	var cmd *exec.Cmd
	isolated := false

	switch {
	case t.isolator != nil && t.isolator.Enabled():
		selfExe, exeErr := os.Executable()
		if exeErr != nil {
			return Result{}, &os.PathError{Op: "resolve self executable for isolation", Path: "", Err: exeErr}
		}
		wrapped := t.isolator.Wrap(selfExe, command, args)
		// Rebuild through CommandContext so timeout plumbing matches the
		// direct path; Wrap output is deterministic (Path/Args/Env only),
		// so nothing else needs copying.
		cmd = exec.CommandContext(execCtx, wrapped.Path, wrapped.Args[1:]...)
		cmd.Env = wrapped.Env
		isolated = true

	case t.requireIsolation && runtime.GOOS == "linux":
		return Result{
			Content:  "DENIED: shell isolation required but unavailable",
			Metadata: map[string]any{"denied": true},
		}, nil

	default:
		if t.logger != nil {
			t.legacyNoteOnce.Do(func() {
				t.logger.Debug("os isolation unavailable on this platform; permissions model only")
			})
		}
		cmd = exec.CommandContext(execCtx, command, args...)
	}

	if workdir := req.Workdir; workdir != "" {
		resolved, wdErr := validateWorkdir(workdir)
		if wdErr != nil {
			return Result{Content: "ERROR: " + wdErr.Error()}, nil
		}
		cmd.Dir = resolved
	}

	// Kill the whole child tree on timeout, not just the direct child:
	// children run as their own process group (Unix), and Cancel overrides
	// CommandContext's single-process kill accordingly. WaitDelay guards
	// against grandchildren holding the output pipes open after the kill.
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		_ = killProcessTree(cmd.Process) // best-effort; may race exit
		return nil
	}
	cmd.WaitDelay = 3 * time.Second

	// Capture combined output
	output, err := cmd.CombinedOutput()

	// Check for timeout - context may be cancelled even if err is ExitError (Windows)
	timedOut := errors.Is(err, context.DeadlineExceeded) || execCtx.Err() == context.DeadlineExceeded
	// On Windows, process termination due to timeout may result in ExitError
	// Check if context is done and error is not a normal exit
	if !timedOut && execCtx.Err() != nil {
		// Context was cancelled (timeout or cancellation), treat as timeout
		timedOut = true
	}

	// Truncate output to 50KB
	const maxOutput = 50 * 1024
	truncated := false
	if len(output) > maxOutput {
		output = output[:maxOutput]
		truncated = true
	}

	exitCode := 0
	if err != nil {
		// Check for timeout first - if context was cancelled, treat as timeout
		if timedOut {
			exitCode = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -2
		}
	}

	metadata := map[string]any{
		"exit_code":   exitCode,
		"duration_ms": int64(0),
		"truncated":   truncated,
	}
	if isolated {
		metadata["isolated"] = true
	}
	if timedOut {
		metadata["timeout"] = true
	}

	content := string(output)

	return Result{Content: content, Metadata: metadata}, nil
}
