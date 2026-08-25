// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"context"
	"os/exec"
	"time"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// gitTool implements the git tool with subcommand dispatch.
type gitTool struct{}

func newGitTool() *gitTool { return &gitTool{} }

func (t *gitTool) Name() string { return "git" }
func (t *gitTool) Description() string {
	return "Execute a git subcommand. Allowed subcommands are controlled by permissions. Destructive operations are blocked by the safety floor."
}

func (t *gitTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subcommand": map[string]any{
				"type":        "string",
				"description": "Git subcommand to execute (e.g., status, add, commit, log, diff)",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Arguments to pass to the git subcommand",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for git execution (default: workspace root)",
			},
		},
		"required": []string{"subcommand"},
	}
}

func (t *gitTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	subcommand := req.Subcommand
	args := req.GitArgs
	workdir := req.Workdir

	// Create context with timeout (60s for git)
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Build git command: git -C <workdir> <subcommand> <args...>
	gitArgs := []string{}
	if workdir != "" {
		gitArgs = append(gitArgs, "-C", workdir)
	}
	gitArgs = append(gitArgs, subcommand)
	gitArgs = append(gitArgs, args...)

	cmd := exec.CommandContext(execCtx, "git", gitArgs...)

	// Capture combined output
	output, err := cmd.CombinedOutput()

	// Check for timeout
	timedOut := execCtx.Err() == context.DeadlineExceeded

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			exitCode = -2
		}
	}

	durationMs := int64(0) // We don't have precise timing

	metadata := map[string]any{
		"exit_code":   exitCode,
		"duration_ms": durationMs,
		"subcommand":  subcommand,
	}
	if timedOut {
		metadata["timeout"] = true
	}

	content := string(output)

	return Result{Content: content, Metadata: metadata}, nil
}
