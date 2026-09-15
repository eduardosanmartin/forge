package perf

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// TestLongSessionNoDegradation is the dedicated measurement RNF-1.4 asks
// for: "sesiones de larga duración (horas/dÃ­as) sin degradación de
// rendimiento ni fugas de memoria." A single session runs 200 real turns
// through the same agent loop/store/context-assembler path as
// TestTurnOverhead (RNF-1.2) — the context window keeps each turn's prompt
// bounded regardless of how long the session has run, so per-turn overhead
// and heap usage late in the session should look statistically like early
// in the session, not grow with turn count.
//
// HONEST SCOPE, stated rather than assumed: 200 turns against a
// zero-latency mock server in one test process is a proxy for "hours/days"
// of real usage, not a literal multi-hour soak test — that would make this
// suite impractical to run routinely (it already skips under -short).
// What it DOES faithfully exercise is the part most likely to degrade with
// session length: repeated context assembly, SQLite append/read growth,
// and steady-state heap behavior over many turns on one growing session.
func TestLongSessionNoDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("long-session benchmark is integration-scope; skipping in -short")
	}
	ctx := context.Background()
	cfg := perfConfig(newMockChatServer(t).URL)
	logger := perfLogger(t)
	workspace := tempWorkspace(t)

	permsEng, err := perms.New(perfPermissionsPolicy(), workspace, logger)
	if err != nil {
		t.Fatalf("create permission engine: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	embStore, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("create embedding store: %v", err)
	}
	t.Cleanup(func() { _ = embStore.Close() })
	retriever := retrieval.NewRetriever(embStore)
	compactor := compaction.NewCompactor(compaction.Config{SummaryCharsPerMessage: 60})
	if err := anchor.CreateAnchorTable(ctx, st.DB()); err != nil {
		t.Fatalf("create anchors table: %v", err)
	}
	anchorStore := anchor.NewAnchorStoreSQL(st.DB())

	llmReg, err := llm.New(cfg, cfg.Network.AllowedHosts, logger)
	if err != nil {
		t.Fatalf("create llm registry: %v", err)
	}
	t.Cleanup(func() { _ = llmReg.Close() })

	toolsReg := tools.NewDefaultRegistryWithDeps(permsEng, workspace, logger, retriever, compactor, anchorStore)
	ag := agent.NewAgent(cfg, st, llmReg, toolsReg, permsEng, logger)

	sess, err := st.CreateSession(ctx, map[string]any{"purpose": "perf-long-session"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	const (
		turns       = 200
		windowSize  = 25 // compare the first/last windowSize turns
		sampleEvery = 20
	)
	overhead := make([]float64, 0, turns)
	var heapSamples []uint64

	for i := 0; i < turns; i++ {
		result, err := ag.ExecuteTurn(ctx, sess.ID, "describe the current state of the widget subsystem in one sentence")
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
		if result.Error != nil {
			t.Fatalf("turn %d: turn error: %v", i+1, result.Error)
		}
		overhead = append(overhead, float64(result.Metrics.HarnessOverheadMs))

		if i%sampleEvery == 0 {
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			heapSamples = append(heapSamples, ms.HeapInuse)
		}
	}

	earlyMedian := medianOf(overhead[:windowSize])
	lateMedian := medianOf(overhead[len(overhead)-windowSize:])
	t.Logf("harness overhead over %d turns: early(median of first %d)=%.1fms late(median of last %d)=%.1fms",
		turns, windowSize, earlyMedian, windowSize, lateMedian)

	// Generous ratio: this is a degradation smoke test, not a tight
	// regression gate — real noise (GC pauses, scheduler jitter) can
	// legitimately move a 1-5ms median by a large relative factor while
	// staying well under the RNF-1.2 50ms target the whole time. What it
	// catches is a genuine growth trend (e.g. an unbounded history scan).
	if earlyMedian > 1 && lateMedian > earlyMedian*4 {
		t.Errorf("late-session overhead %.1fms is > 4x early-session overhead %.1fms — possible performance degradation over a long session (RNF-1.4)",
			lateMedian, earlyMedian)
	}
	if lateMedian >= 50 {
		t.Errorf("late-session harness overhead %.1fms >= 50ms RNF-1.2 target, even after %d turns", lateMedian, turns)
	}

	if len(heapSamples) >= 2 {
		first, last := heapSamples[0], heapSamples[len(heapSamples)-1]
		t.Logf("heap in-use across the session: first sample=%.1fMB last sample=%.1fMB (n=%d samples)",
			mib(first), mib(last), len(heapSamples))
		// Same spirit as the overhead check: catch real unbounded growth
		// (a leak) without false-alarming on normal steady-state variance
		// from more session rows/history now resident.
		if last > first*5 && last > 20<<20 {
			t.Errorf("heap in-use grew from %.1fMB to %.1fMB over %d turns — possible memory leak over a long session (RNF-1.4)",
				mib(first), mib(last), turns)
		}
	}
}

func medianOf(vals []float64) float64 {
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}
