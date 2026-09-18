package cli

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func newSetProviderCommand() *cobra.Command {
	var model string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "set-provider <name>",
		Short: "Switch the daemon's default LLM provider (and optionally model)",
		Long: "Switches the running daemon's default provider. Without --model, prints that\n" +
			"provider's LIVE model catalog (forced fresh from its own /models endpoint —\n" +
			"includes models not declared in providers.<name>.models) and changes nothing,\n" +
			"so you can see what's actually available before picking one:\n" +
			"  forge daemon set-provider anthropic\n" +
			"  forge daemon set-provider anthropic --model claude-opus-5\n\n" +
			"Not tied to any session — every session created after this picks up the new\n" +
			"default. For an interactive pick-from-a-list flow, use /provider in `forge chat`\n" +
			"instead; for a one-off override on a single existing session, `/model\n" +
			"provider/model` (or the session.switch_model RPC) changes both at once too.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetProvider(cmd.Context(), args[0], model, jsonOut)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "switch to this model too (omit to just list what's available)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSetProvider(ctx context.Context, provider, model string, jsonOut bool) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	if model == "" {
		var res daemon.ProviderListModelsResult
		if err := cl.Call(ctx, daemon.MethodProviderListModels,
			daemon.ProviderListModelsParams{Provider: provider}, &res); err != nil {
			return fmt.Errorf("list models for provider %q: %w", provider, err)
		}
		sort.Strings(res.Models)
		if jsonOut {
			return writeJSONResultEnvelope(os.Stdout, "daemon", res)
		}
		if len(res.Models) == 0 {
			fmt.Printf("provider %q has no models available\n", provider)
			return nil
		}
		fmt.Printf("models available on %q (pass --model to switch):\n", provider)
		for _, m := range res.Models {
			fmt.Printf("  %s\n", m)
		}
		return nil
	}

	var res map[string]any
	if err := cl.Call(ctx, daemon.MethodProviderSwitch,
		daemon.ProviderSwitchParams{Provider: provider, Model: model}, &res); err != nil {
		return fmt.Errorf("switch to %s/%s: %w", provider, model, err)
	}
	if jsonOut {
		return writeJSONResultEnvelope(os.Stdout, "daemon", res)
	}
	fmt.Printf("default provider switched to %s/%s\n", provider, model)
	return nil
}
