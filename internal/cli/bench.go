package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/eduardosanmartin/forge/internal/benchlive"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newBenchCommand())
}

func newBenchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Live performance bench against a real local model (RNF-10)",
	}
	cmd.AddCommand(newBenchLiveCommand(), newBenchCompareCommand())
	return cmd
}

func newBenchLiveCommand() *cobra.Command {
	var (
		baseURL, model, profile, outPath, sizes string
		numCtx, numPredict                      int
		jsonOut                                 bool
	)
	cmd := &cobra.Command{
		Use:   "live",
		Short: "Measure tokens/s, TTFT, prefill per context size, KV-cache reuse and task wall time",
		Long: "Runs against Ollama's native API (default http://127.0.0.1:11434), which reports\n" +
			"server-side prefill and generation timings. Results are per hardware profile\n" +
			"(RNF-10.2): run once on each machine with --profile A or B and keep the JSON\n" +
			"files apart; compare runs of the same profile with `forge bench compare`.\n" +
			"The base URL's host must be in network.allowed_hosts (RNF-4.9).",
		RunE: func(cmd *cobra.Command, args []string) error {
			var allowed []string
			if app, ok := AppFromContext(cmd.Context()); ok && app != nil && app.Config != nil {
				allowed = app.Config.Network.AllowedHosts
			} else {
				allowed = config.Defaults().Network.AllowedHosts
			}
			var ctxSizes []int
			if strings.TrimSpace(sizes) != "" {
				for _, f := range strings.Split(sizes, ",") {
					n, err := strconv.Atoi(strings.TrimSpace(f))
					if err != nil || n <= 0 {
						return &UsageError{Err: fmt.Errorf("--sizes: %q is not a positive integer", f)}
					}
					ctxSizes = append(ctxSizes, n)
				}
			}
			out := cmd.ErrOrStderr()
			res, err := benchlive.Run(cmd.Context(), benchlive.Config{
				BaseURL:      baseURL,
				Model:        model,
				Profile:      strings.ToUpper(profile),
				ContextSizes: ctxSizes,
				NumCtx:       numCtx,
				NumPredict:   numPredict,
				AllowedHosts: allowed,
				Progress:     func(line string) { fmt.Fprintln(out, "  "+line) },
			})
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(res, "", "  ")
			if outPath != "" {
				if err := os.WriteFile(outPath, append(data, '\n'), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", outPath, err)
				}
			}
			if jsonOut {
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			s := res.Summary
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "\nforge bench live — %s, profile %s, %s/%s %d CPU %s\n", res.Model, res.Profile, res.Hardware.OS, res.Hardware.Arch, res.Hardware.NumCPU, res.Hardware.CPUModel)
			fmt.Fprintf(w, "  generation:     %.1f tok/s\n", s.GenTokPerS)
			fmt.Fprintf(w, "  median TTFT:    %.0f ms (tasks)\n", s.MedianTTFTMs)
			fmt.Fprintf(w, "  prefill:        %.1f tok/s (4k: %.0f ms, 8k: %.0f ms)\n", s.PrefillTokPerS, s.Prefill4kMs, s.Prefill8kMs)
			fmt.Fprintf(w, "  KV-cache reuse: TTFT %.1fx faster on a repeated ~4k prefix\n", s.KVCacheSpeedup)
			fmt.Fprintf(w, "  task set:       %.0f ms wall (%d tasks)\n", s.TasksWallMs, len(res.Tasks))
			if outPath != "" {
				fmt.Fprintf(w, "  saved:          %s\n", outPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", "http://127.0.0.1:11434", "Ollama base URL (native API, not /v1)")
	cmd.Flags().StringVar(&model, "model", "", "model name as Ollama lists it (required)")
	cmd.Flags().StringVar(&profile, "profile", "A", "hardware profile label: A (interactive/standard) or B (batch)")
	cmd.Flags().StringVar(&outPath, "out", "", "write the result JSON to this file")
	cmd.Flags().StringVar(&sizes, "sizes", "", "comma-separated prompt sizes in tokens for the prefill curve (default 1024,2048,4096,8192)")
	cmd.Flags().IntVar(&numCtx, "num-ctx", 16384, "context window for every call (fixed for the run: changing it reloads the model)")
	cmd.Flags().IntVar(&numPredict, "num-predict", 256, "max generated tokens per task")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result JSON on stdout")
	_ = cmd.MarkFlagRequired("model")
	return cmd
}

func newBenchCompareCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <baseline.json> <candidate.json>",
		Short: "Compare two `forge bench live` results",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := benchlive.Load(args[0])
			if err != nil {
				return err
			}
			b, err := benchlive.Load(args[1])
			if err != nil {
				return err
			}
			benchlive.WriteComparison(cmd.OutOrStdout(), a, b)
			return nil
		},
	}
}
