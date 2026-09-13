package perf

import (
	"context"
	"fmt"
	"path/filepath"
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

// TestTurnOverhead pins per-turn harness overhead (RNF-1.2, spec target
// < 50ms excluding LLM inference time). The turn runs through the REAL
// agent loop, real store (SQLite appends on a temp database), real tools
// and permission engines, and the real llm registry/provider construction
// path — the only external dependency is a zero-latency local
// httptest server answering an OpenAI-format chat completion, so
// TurnMetrics.HarnessOverheadMs (~ duration minus total LLM wait time) is
// effectively pure harness work: deterministic DB appends plus JSON
// marshaling.
//
// Twenty turns over one session are sampled and reported (p50 and max via
// t.Logf). The assertion checks p50 < 50ms: the real spec target with high
// headroom and low flake risk for this workload.
func TestTurnOverhead(t *testing.T) {
	if testing.Short() {
		t.Skip("turn overhead benchmark is integration-scope; skipping in -short")
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

	sess, err := st.CreateSession(ctx, map[string]any{"purpose": "perf-overhead"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	const turns = 20
	overhead := make([]float64, 0, turns)
	for i := 0; i < turns; i++ {
		result, err := ag.ExecuteTurn(ctx, sess.ID, fmt.Sprintf("turn %d", i+1))
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
		if result.Error != nil {
			t.Fatalf("turn %d: turn error: %v", i+1, result.Error)
		}
		overhead = append(overhead, float64(result.Metrics.HarnessOverheadMs))
	}

	sort.Float64s(overhead)
	p50 := overhead[len(overhead)/2]
	max := overhead[len(overhead)-1]
	t.Logf("per-turn harness overhead p50: %.1f ms, max: %.1f ms, n=%d (spec RNF-1.2 target < 50ms)", p50, max, turns)
	if p50 >= 50 {
		t.Errorf("per-turn harness overhead p50 %.1f ms >= 50 ms threshold (RNF-1.2)", p50)
	}
}
