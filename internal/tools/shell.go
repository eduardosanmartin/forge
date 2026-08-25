// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// shellExecTool implements the shell.exec tool.
type shellExecTool struct{}

func newShellExecTool() *shellExecTool { return &shellExecTool{} }

func (t *shellExecTool) Name() string { return "shell.exec" }
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
				"description": "Working directory for command execution",
			},
		},
		"required": []string{"command"},
	}
}

func (t *shellExecTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	command := req.Command
	args := req.Args
	timeoutSec := req.TimeoutSec
	workdir := req.Workdir

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

	cmd := exec.CommandContext(execCtx, command, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

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

	durationMs := int64(0)
	// We don't have precise timing from exec.CommandContext, but we can approximate
	// For now, we'll leave it as 0 since we don't have start time

	metadata := map[string]any{
		"exit_code":   exitCode,
		"duration_ms": durationMs,
		"truncated":   truncated,
	}
	if timedOut {
		metadata["timeout"] = true
	}

	content := string(output)

	return Result{Content: content, Metadata: metadata}, nil
}
