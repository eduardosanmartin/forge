package cli

import (
	"fmt"

	"github.com/eduardosanmartin/forge/internal/version"

	"github.com/spf13/cobra"
)

// newVersionCommand builds the `forge version` subcommand, which prints the
// build banner produced by internal/version.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the forge version.",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return nil
		},
	}
}
