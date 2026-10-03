package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// TestLiveCompactionSummaries measures RF-3.3 against a real local model:
// the size of the compacted view with deterministic vs LLM summaries, and
// what generating the summaries costs. Opt-in:
//
//	FORGE_LIVE_COMPACTION=1 [FORGE_LIVE_SUMMARY_MODEL=qwen2.5-coder:1.5b] \
//	[FORGE_LIVE_TURNS=40] go test -run TestLiveCompactionSummaries -v ./internal/agent
//
// The transcript is synthetic but shaped like real work: each turn is a
// request, a tool call, a tool result quoting real source from this repo,
// and an answer. Token counts are chars/4 estimates (same for both views).
func TestLiveCompactionSummaries(t *testing.T) {
	if os.Getenv("FORGE_LIVE_COMPACTION") == "" {
		t.Skip("set FORGE_LIVE_COMPACTION=1 to run against a local Ollama")
	}
	model := envOr("FORGE_LIVE_SUMMARY_MODEL", "qwen2.5-coder:1.5b")
	turns := 40
	if v := os.Getenv("FORGE_LIVE_TURNS"); v != "" {
		_, _ = fmt.Sscan(v, &turns)
	}
	ctx := context.Background()

	cfg := config.Defaults()
	cfg.DefaultProvider = "ollama"
	cfg.Providers = map[string]config.Provider{"ollama": {
		Kind: "openai-compatible", BaseURL: envOr("FORGE_LIVE_BASE_URL", "http://127.0.0.1:11434/v1"),
		Models: []string{model},
	}}
	reg, err := llm.New(cfg, []string{"127.0.0.1", "localhost"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	provider, _ := reg.GetDefault()

	st, err := store.Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sessionID := seedRealisticSession(t, st, turns)

	var mu sync.Mutex
	var calls []liveSummaryCall
	summarize := func(ctx context.Context, prompt string) (string, error) {
		temp, maxTok := 0.0, 320
		start := time.Now()
		resp, err := provider.Chat(ctx, llm.ChatRequest{
			Model: model, Messages: []llm.Message{{Role: "user", Content: prompt}},
			Temperature: &temp, MaxTokens: &maxTok,
		})
		if err != nil {
			return "", err
		}
		c := liveSummaryCall{dur: time.Since(start), merge: strings.Contains(prompt, "CONSECUTIVE SUMMARIES")}
		if resp.Usage != nil {
			c.promptTok, c.outTok = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		return resp.Choices[0].Message.Content, nil
	}

	compactor := compaction.NewCompactor(compaction.Config{SummaryCharsPerMessage: 60}) // daemon setting
	if err := compaction.CreateSummaryTable(ctx, st.DB()); err != nil {
		t.Fatal(err)
	}
	h := compaction.NewHierarchy(compaction.NewSQLSummaryCache(st.DB()), summarize, model, 4, compactor.SummarizeBlock)

	det := NewContextAssembler(tools.New(nil, "", nil), st, 8)
	det.SetV1Deps(V1Deps{Compactor: compactor})
	withLLM := NewContextAssembler(tools.New(nil, "", nil), st, 8)
	withLLM.SetV1Deps(V1Deps{Compactor: compactor, Summaries: h})

	detView, detTotal := viewSize(t, det, sessionID)

	start := time.Now()
	if err := withLLM.PrecomputeSummaries(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	precompute := time.Since(start)
	llmView, llmTotal := viewSize(t, withLLM, sessionID)

	var l1, l2 []liveSummaryCall
	for _, c := range calls {
		if c.merge {
			l2 = append(l2, c)
		} else {
			l1 = append(l1, c)
		}
	}
	t.Logf("model %s, %d turns (%d messages)", model, turns, turns*4+1)
	t.Logf("compacted view: deterministic %d entries ~%d tok | LLM %d entries ~%d tok",
		len(detView), tok(detView), len(llmView), tok(llmView))
	t.Logf("whole prompt:   deterministic ~%d tok | LLM ~%d tok", detTotal/4, llmTotal/4)
	t.Logf("precompute: %s total; level-1 %s; level-2+ %s", precompute.Round(time.Second), callStats(l1), callStats(l2))
	if len(detView) > 0 && len(llmView) > 0 {
		t.Logf("sample deterministic entry (first 400 chars):\n%s", firstN(detView[0], 400))
		t.Logf("sample LLM entry:\n%s", llmView[0])
	}
}

type liveSummaryCall struct {
	dur               time.Duration
	promptTok, outTok int
	merge             bool
}

func callStats(cs []liveSummaryCall) string {
	if len(cs) == 0 {
		return "none"
	}
	durs := make([]time.Duration, len(cs))
	maxPrompt := 0
	for i, c := range cs {
		durs[i] = c.dur
		maxPrompt = max(maxPrompt, c.promptTok)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return fmt.Sprintf("%d calls, median %s, max %s, max prompt %d tok",
		len(cs), durs[len(durs)/2].Round(100*time.Millisecond), durs[len(durs)-1].Round(100*time.Millisecond), maxPrompt)
}

func viewSize(t *testing.T, a *ContextAssembler, sessionID string) ([]string, int) {
	t.Helper()
	msgs, err := a.Build(context.Background(), sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	var view []string
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		if strings.HasPrefix(m.Content, "COMPACTED HISTORY (v1):") {
			view = append(view, m.Content)
		}
	}
	return view, total
}

func tok(entries []string) int {
	n := 0
	for _, e := range entries {
		n += len(e)
	}
	return n / 4
}

func firstN(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// seedRealisticSession appends turns shaped like real agent work, quoting
// this repository's own source as tool output.
func seedRealisticSession(t *testing.T, st *store.Store, turns int) string {
	t.Helper()
	ctx := context.Background()
	session, err := st.CreateSession(ctx, map[string]any{"v1_compaction": true})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join("..", "*", "*.go"))
	sort.Strings(files)
	var sources []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if b, err := os.ReadFile(f); err == nil && len(b) > 2000 {
			sources = append(sources, f)
		}
	}
	if len(sources) == 0 {
		t.Fatal("no source files found to quote")
	}
	add := func(role, content string) {
		if _, _, err := st.AppendMessage(ctx, &store.Message{SessionID: session.ID, Role: role, Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < turns; i++ {
		f := sources[i%len(sources)]
		b, _ := os.ReadFile(f)
		name := filepath.ToSlash(f)
		add("user", fmt.Sprintf("Look at %s and tell me what the main exported function does; then suggest one improvement.", name))
		add("assistant", fmt.Sprintf("I'll read %s first.", name))
		add("tool", string(b[:min(len(b), 1500)]))
		add("assistant", fmt.Sprintf("%s mainly defines the logic shown above. Its main exported function validates its "+
			"input, does the work and returns an error on failure. Improvement: add a table-driven test covering the "+
			"error paths, and document the invariants at the top of the file.", name))
	}
	add("user", "Now summarize everything we changed so far.")
	return session.ID
}
