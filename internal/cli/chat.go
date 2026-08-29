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
	var (
		sessionID        string
		enableRetrieval  bool
		enableCompaction bool
		enableAnchoring  bool
		enableRouting    bool
	)
	cmd := &cobra.Command{
		Use:   "chat [--session <id>] [--retrieval] [--compaction] [--anchoring] [--routing]",
		Short: "Interactive REPL against the running forge daemon",
		Long: "Opens an interactive session against the daemon started with " +
			"'forge serve'. Type messages or /help inside the REPL; /exit or " +
			"Ctrl-D quits.\n\n" +
			"V1 features (opt-in):\n" +
			"  --retrieval    Enable selective context retrieval (RF-3.2)\n" +
			"  --compaction   Enable hierarchical conversation compaction (RF-3.3)\n" +
			"  --anchoring    Enable persistent anchored facts (RF-3.4/3.5)\n" +
			"  --routing      Enable cost-based model routing per step (RF-2.4/2.5)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(cmd.Context(), sessionID,
				enableRetrieval, enableCompaction, enableAnchoring, enableRouting)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "attach to an existing session id (default: start a new session)")
	cmd.Flags().BoolVar(&enableRetrieval, "retrieval", false, "enable selective context retrieval (v1)")
	cmd.Flags().BoolVar(&enableCompaction, "compaction", false, "enable hierarchical compaction (v1)")
	cmd.Flags().BoolVar(&enableAnchoring, "anchoring", false, "enable persistent anchored facts (v1)")
	cmd.Flags().BoolVar(&enableRouting, "routing", false, "enable cost-based model routing (v1)")
	return cmd
}

func runChat(ctx context.Context, sessionID string,
	enableRetrieval, enableCompaction, enableAnchoring, enableRouting bool) error {

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	repl := client.NewREPL(cl, sessionID, os.Stdout, os.Stdin,
		client.REPLOptions{
			EnableRetrieval:  enableRetrieval,
			EnableCompaction: enableCompaction,
			EnableAnchoring:  enableAnchoring,
			EnableRouting:    enableRouting,
		})
	if err := repl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("repl: %w", err)
	}
	return nil
}