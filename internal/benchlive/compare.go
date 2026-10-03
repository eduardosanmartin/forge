package benchlive

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// Delta is one compared metric. HigherIsBetter tells how to read DeltaPct.
type Delta struct {
	Metric         string  `json:"metric"`
	A              float64 `json:"a"`
	B              float64 `json:"b"`
	DeltaPct       float64 `json:"delta_pct"`
	HigherIsBetter bool    `json:"higher_is_better"`
}

// Load reads a result JSON file written by a previous run.
func Load(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if r.Schema != SchemaVersion {
		return nil, fmt.Errorf("%s: schema %d, this build reads %d", path, r.Schema, SchemaVersion)
	}
	return &r, nil
}

// Compare lists the headline metrics of a (baseline) and b (candidate).
func Compare(a, b *Result) []Delta {
	rows := []Delta{
		{Metric: "gen_tok_per_s", A: a.Summary.GenTokPerS, B: b.Summary.GenTokPerS, HigherIsBetter: true},
		{Metric: "median_ttft_ms", A: a.Summary.MedianTTFTMs, B: b.Summary.MedianTTFTMs},
		{Metric: "prefill_tok_per_s", A: a.Summary.PrefillTokPerS, B: b.Summary.PrefillTokPerS, HigherIsBetter: true},
		{Metric: "prefill_4k_ms", A: a.Summary.Prefill4kMs, B: b.Summary.Prefill4kMs},
		{Metric: "prefill_8k_ms", A: a.Summary.Prefill8kMs, B: b.Summary.Prefill8kMs},
		{Metric: "kv_cache_ttft_speedup", A: a.Summary.KVCacheSpeedup, B: b.Summary.KVCacheSpeedup, HigherIsBetter: true},
		{Metric: "tasks_wall_ms", A: a.Summary.TasksWallMs, B: b.Summary.TasksWallMs},
	}
	for i := range rows {
		if rows[i].A != 0 {
			rows[i].DeltaPct = 100 * (rows[i].B - rows[i].A) / rows[i].A
		}
	}
	return rows
}

// WriteComparison prints Compare's rows as a table, flagging whether each
// change is an improvement.
func WriteComparison(w io.Writer, a, b *Result) {
	fmt.Fprintf(w, "A: %s profile %s (%s, %d CPU)\nB: %s profile %s (%s, %d CPU)\n\n",
		a.Model, a.Profile, a.Hardware.CPUModel, a.Hardware.NumCPU,
		b.Model, b.Profile, b.Hardware.CPUModel, b.Hardware.NumCPU)
	if a.Profile != b.Profile {
		fmt.Fprintln(w, "note: different hardware profiles — RNF-10.2 compares within a profile; read this as a profile gap, not a regression")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "metric\tA\tB\tdelta\t")
	for _, d := range Compare(a, b) {
		verdict := ""
		switch {
		case d.A == 0 || d.B == 0:
			verdict = "n/a"
		case (d.DeltaPct > 0) == d.HigherIsBetter && d.DeltaPct != 0:
			verdict = "better"
		case d.DeltaPct != 0:
			verdict = "worse"
		}
		fmt.Fprintf(tw, "%s\t%.1f\t%.1f\t%+.1f%%\t%s\n", d.Metric, d.A, d.B, d.DeltaPct, verdict)
	}
	tw.Flush()
}
