package cli

import (
	"context"
	"encoding/json"
	"errors"
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
		jsonOut        bool
		sessionID      string
		enableRetrieval  bool
		enableCompaction bool
		enableAnchoring  bool
		enableRouting    bool
	)
	cmd := &cobra.Command{
		Use:   "run [--json] [--session <id>] [--retrieval] [--compaction] [--anchoring] [--routing] <prompt>",
		Short: "Execute one prompt non-interactively through the daemon",
		Long: "Runs a single agent turn without entering the interactive REPL. " +
			"With --json only the structured OneShotResult is printed on stdout.\n\n" +
			"V1 features (opt-in):\n" +
			"  --retrieval    Enable selective context retrieval (RF-3.2)\n" +
			"  --compaction   Enable hierarchical conversation compaction (RF-3.3)\n" +
			"  --anchoring    Enable persistent anchored facts (RF-3.4/3.5)\n" +
			"  --routing      Enable cost-based model routing per step (RF-2.4/2.5)\n" +
			"                 (this build: selects the generation-step model from\n" +
			"                 providers.<name>.model_roles; other steps are deterministic\n" +
			"                 and need no model)",
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
			return runRun(cmd.Context(), args[0], sessionID, jsonOut,
				enableRetrieval, enableCompaction, enableAnchoring, enableRouting)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print only the machine-readable JSON result on stdout")
	cmd.Flags().StringVar(&sessionID, "session", "", "reuse an existing session id (default: ephemeral session)")
	cmd.Flags().BoolVar(&enableRetrieval, "retrieval", false, "enable selective context retrieval (v1)")
	cmd.Flags().BoolVar(&enableCompaction, "compaction", false, "enable hierarchical compaction (v1)")
	cmd.Flags().BoolVar(&enableAnchoring, "anchoring", false, "enable persistent anchored facts (v1)")
	cmd.Flags().BoolVar(&enableRouting, "routing", false, "enable cost-based model routing (v1)")
	return cmd
}

func runRun(ctx context.Context, prompt, sessionID string, jsonOut bool,
	enableRetrieval, enableCompaction, enableAnchoring, enableRouting bool) error {

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	res, err := client.RunOneShot(ctx, cl, prompt, client.RunOptions{
		SessionID:        sessionID,
		EnableRetrieval:  enableRetrieval,
		EnableCompaction: enableCompaction,
		EnableAnchoring:  enableAnchoring,
		EnableRouting:    enableRouting,
	})
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

func writeJSONResult(out io.Writer, res *client.OneShotResult) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

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

func previewToolArgs(args json.RawMessage) string {
	return client.FormatToolArgs(args)
}

// daemonHint enriches connectivity failures with the actionable fix.
func daemonHint(err error) error {
	if errors.Is(err, client.ErrDaemonNotRunning) {
		return fmt.Errorf("%w\nhint: start the daemon first with 'forge serve'", err)
	}
	return err
}