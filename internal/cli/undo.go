package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eduardosanmartin/forge/internal/snapshot"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newUndoCommand())
}

// openSnapshots opens the shadow snapshot store of workspaceRoot under
// ~/.forge/snapshots.
func openSnapshots(workspaceRoot string) (*snapshot.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return snapshot.Open(filepath.Join(home, ".forge", "snapshots"), workspaceRoot)
}

func newUndoCommand() *cobra.Command {
	var list bool
	var to string
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Roll the workspace's files back to before an agent turn (F4)",
		Long: "The daemon snapshots the workspace before the first file-changing tool call of\n" +
			"each agent turn, into a shadow git repository under ~/.forge/snapshots (your\n" +
			"project's own repository, index and history are never touched).\n\n" +
			"  forge undo           restore the most recent snapshot (undo the last turn)\n" +
			"  forge undo --list    list snapshots, newest first\n" +
			"  forge undo --to N    restore snapshot N from --list (or a commit hash)\n\n" +
			"The state right before the undo is snapshotted first, so an undo can itself be\n" +
			"undone. Only files inside the workspace are covered: side effects of shell\n" +
			"commands elsewhere (installed packages, databases, network calls) are not.",
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			store, err := openSnapshots(wd)
			if err != nil {
				return err
			}
			entries, err := store.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if list {
				if len(entries) == 0 {
					fmt.Fprintln(out, "No snapshots for this workspace yet.")
					return nil
				}
				for i, e := range entries {
					fmt.Fprintf(out, "%3d  %s  %s  %s\n", i+1, e.Commit[:10], e.CreatedAt.Local().Format("2006-01-02 15:04:05"), e.Label)
				}
				return nil
			}
			if len(entries) == 0 {
				return &UsageError{Err: fmt.Errorf("no snapshots for this workspace yet (they are taken before an agent turn changes files)")}
			}
			target := entries[0].Commit
			if to != "" {
				if n, err := strconv.Atoi(to); err == nil {
					if n < 1 || n > len(entries) {
						return &UsageError{Err: fmt.Errorf("--to %d: out of range (1-%d, see --list)", n, len(entries))}
					}
					target = entries[n-1].Commit
				} else {
					target = strings.TrimSpace(to)
				}
			}
			safety, err := store.Restore(cmd.Context(), target)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Workspace restored to snapshot %s.\nThe previous state was saved as %s — `forge undo --to %s` brings it back.\n", target[:10], safety[:10], safety[:10])
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list snapshots instead of restoring")
	cmd.Flags().StringVar(&to, "to", "", "snapshot to restore: a number from --list or a commit hash")
	return cmd
}
