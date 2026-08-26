package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eduardosanmartin/forge/internal/client"

	"github.com/spf13/cobra"
)

// UsageError marks command-invocation mistakes (bad flags or arguments).
// The binary entrypoint translates it into exit code 2, keeping runtime
// failures at 1.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string {
	if e.Err == nil {
		return "usage error"
	}
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error { return e.Err }

func usageErrorf(format string, args ...any) *UsageError {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

func init() {
	RootCommand.AddCommand(newRunCommand())
}

func newRunCommand() *cobra.Command {
	var (
		jsonOut   bool
		sessionID string
	)
	cmd := &cobra.Command{
		Use:   "run [--json] [--session <id>] <prompt>",
		Short: "Execute one prompt non-interactively through the daemon",
		Long: "Runs a single agent turn without entering the interactive REPL. " +
			"With --json only the structured OneShotResult is printed on stdout.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageErrorf("run accepts exactly 1 prompt argument, got %d", len(args))
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageErrorf("prompt must not be empty")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd.Context(), args[0], sessionID, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print only the machine-readable JSON result on stdout")
	cmd.Flags().StringVar(&sessionID, "session", "", "reuse an existing session id (default: ephemeral session)")
	return cmd
}

// runRun executes the non-interactive flow: connect, one turn, print.
func runRun(ctx context.Context, prompt, sessionID string, jsonOut bool) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	res, err := client.RunOneShot(ctx, cl, prompt, client.RunOptions{SessionID: sessionID})
	if err != nil {
		return fmt.Errorf("one-shot turn failed: %w", err)
	}

	if jsonOut {
		if err := writeJSONResult(os.Stdout, res); err != nil {
			return fmt.Errorf("write JSON result: %w", err)
		}
		return nil
	}

	writeHumanResult(os.Stdout, os.Stderr, res)
	return nil
}

// writeJSONResult prints exactly the indented result document on stdout.
func writeJSONResult(out io.Writer, res *client.OneShotResult) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// writeHumanResult prints the final answer on stdout and the compact tool
// trace on stderr so pipes receive clean answer text only.
func writeHumanResult(stdout, stderr io.Writer, res *client.OneShotResult) {
	for _, tc := range res.ToolCalls {
		fmt.Fprintf(stderr, "-> %s(%s)\n", tc.Name, previewToolArgs(tc.Args))
		if tc.OK {
			fmt.Fprintln(stderr, "<- ok")
		} else {
			fmt.Fprintln(stderr, "<- error")
		}
	}
	fmt.Fprintln(stdout, strings.TrimSpace(res.Response))
}

// previewToolArgs renders tool arguments compactly for the stderr trace.
func previewToolArgs(args json.RawMessage) string {
	return client.FormatToolArgs(args)
}
