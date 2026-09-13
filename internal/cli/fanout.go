package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newFanoutCommand())
}

func newFanoutCommand() *cobra.Command {
	var jsonOut bool
	var models string
	var session string
	var maxIter int
	var tokenBudget int
	cmd := &cobra.Command{
		Use:   "fanout <task>",
		Short: "Run the same task on one child per model and compare (RF-9.3)",
		Long:  "Branches one child session per --models entry and executes the task on every child. Each entry is \"provider/model\" or a bare model name (bare names use the default provider). Leaves a parent session behind; run `forge session compare <parent> <child>` for a side-by-side divergence report.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.Join(args, " ")
			entries := parseModelList(models)
			return runFanout(cmd.Context(), cmd.OutOrStdout(), task, entries, session, maxIter, tokenBudget, jsonOut)
		},
	}
	cmd.Flags().StringVar(&models, "models", "", "comma-separated list of \"provider/model\" (or bare model for the default provider); exactly one child is spawned per entry")
	cmd.Flags().StringVar(&session, "session", "", "parent session id (default: create a fresh labeled parent)")
	cmd.Flags().IntVar(&maxIter, "max-iter", 0, "optional max iterations passthrough for every child")
	cmd.Flags().IntVar(&tokenBudget, "token-budget", 0, "optional token budget passthrough for every child")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

// parseModelList splits the comma-separated --models value, trimming
// whitespace and dropping empty entries.
func parseModelList(raw string) []string {
	var out []string
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

func runFanout(ctx context.Context, out io.Writer, task string, models []string, session string, maxIter, tokenBudget int, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	if strings.TrimSpace(task) == "" {
		return fmt.Errorf("task is required")
	}
	if len(models) == 0 {
		return fmt.Errorf("--models is required: provide a comma-separated list of \"provider/model\" entries")
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "fanout", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()

	params := daemon.FanoutParams{
		Task:          task,
		Models:        models,
		SessionID:     session,
		MaxIterations: maxIter,
		TokenBudget:   tokenBudget,
	}
	var res daemon.FanoutResult
	if err := cl.Call(ctx, daemon.MethodFanout, params, &res); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "fanout", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "fanout", res)
	}

	fmt.Fprintf(out, "Fanout parent: %s\n", res.ParentSessionID)
	for _, c := range res.Children {
		label := c.Model
		if c.Provider != "" {
			label = c.Provider + "/" + c.Model
		}
		status := "success"
		if !c.Success {
			status = "failed"
		}
		fmt.Fprintf(out, "\n[%s] %s -> %s\n", status, label, c.ChildSessionID)
		if c.Error != "" {
			fmt.Fprintf(out, "  error: %s\n", c.Error)
		}
		fmt.Fprintf(out, "  summary: %s\n", truncate(c.Summary, 300))
		fmt.Fprintf(out, "  compare: forge session compare %s %s\n", res.ParentSessionID, c.ChildSessionID)
	}
	return nil
}
