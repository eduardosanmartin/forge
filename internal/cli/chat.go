package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/eduardosanmartin/forge/internal/client"

	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newChatCommand())
}

func newChatCommand() *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "chat [--session <id>]",
		Short: "Interactive REPL against the running forge daemon",
		Long: "Opens an interactive session against the daemon started with " +
			"'forge serve'. Type messages or /help inside the REPL; /exit or " +
			"Ctrl-D quits.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(cmd.Context(), sessionID)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "attach to an existing session id (default: start a new session)")
	return cmd
}

// runChat connects to the daemon and hands control to the REPL on
// stdin/stdout. v0 requires an externally running daemon: embedding or
// auto-starting one here would blur process ownership and lifecycle, so the
// error path points at `forge serve` instead.
func runChat(ctx context.Context, sessionID string) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	repl := client.NewREPL(cl, sessionID, os.Stdout, os.Stdin)
	if err := repl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("repl: %w", err)
	}
	return nil
}

// daemonHint enriches connectivity failures with the actionable fix.
func daemonHint(err error) error {
	if errors.Is(err, client.ErrDaemonNotRunning) {
		return fmt.Errorf("%w\nhint: start the daemon first with 'forge serve'", err)
	}
	return err
}
