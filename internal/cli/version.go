package cli

import (
	"fmt"

	"github.com/eduardosanmartin/forge/internal/version"

	"github.com/spf13/cobra"
)

// versionResult is the JSON payload for `forge version --json`.
type versionResult struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Banner  string `json:"banner"`
}

// newVersionCommand builds the `forge version` subcommand, which prints the
// build banner produced by internal/version.
func newVersionCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the forge version.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOut {
				res := versionResult{
					Version: version.Version,
					Commit:  version.Commit,
					Date:    version.Date,
					Banner:  version.String(),
				}
				return writeJSONResultEnvelope(cmd.OutOrStdout(), "version", res)
			}
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}
