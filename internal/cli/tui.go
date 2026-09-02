package cli

import (
	"context"

	"github.com/eduardosanmartin/forge/internal/tui"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newTUICommand())
}

func newTUICommand() *cobra.Command {
	var daemonAddr string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the forge terminal TUI",
		Long:  "Launches the full-screen terminal UI for interacting with the forge daemon.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), daemonAddr)
		},
	}
	cmd.Flags().StringVar(&daemonAddr, "addr", "", "daemon address (default from ~/.forge/daemon.addr)")
	return cmd
}

func runTUI(ctx context.Context, addr string) error {
	return tui.Run(ctx, addr)
}
