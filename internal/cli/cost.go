package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newCostCommand())
}

func newCostCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Estimated token cost metrics (RNF-6.3)",
	}
	cmd.AddCommand(newCostSummaryCommand())
	return cmd
}

func newCostSummaryCommand() *cobra.Command {
	var jsonOut bool
	var limit int
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Estimated cost aggregated across sessions, grouped by provider",
		Long: "See `forge session cost <id>` for the per-session breakdown and the\n" +
			"provider-attribution caveat this inherits. A provider group with any\n" +
			"unpriced session in it reports \"not priced\" rather than an undercounted\n" +
			"dollar figure — see providers.<name>.price_per_million_*_tokens in config.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCostSummary(cmd.Context(), cmd.OutOrStdout(), limit, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().IntVar(&limit, "limit", 0, "max sessions to include (default: daemon's default, currently 200)")
	return cmd
}

func runCostSummary(ctx context.Context, out io.Writer, limit int, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "cost summary", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()

	var res daemon.CostSummaryResult
	if err := cl.Call(ctx, daemon.MethodCostSummary, daemon.CostSummaryParams{Limit: limit}, &res); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "cost summary", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "cost summary", res)
	}

	if len(res.Providers) == 0 {
		fmt.Fprintln(out, "no sessions found")
		return nil
	}
	for _, pc := range res.Providers {
		fmt.Fprintf(out, "%s: %d sessions, %d tokens", pc.Provider, pc.Sessions, pc.TotalTokens)
		if pc.Priced {
			fmt.Fprintf(out, ", estimated $%.4f\n", pc.EstimatedUSD)
		} else {
			fmt.Fprintln(out, ", not priced")
		}
	}
	return nil
}
