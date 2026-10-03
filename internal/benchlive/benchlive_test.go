package benchlive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOllama answers /api/chat with a streamed NDJSON reply whose
// counters depend on prompt length, and simulates KV-cache reuse: a prompt
// identical to the previous one reports only a handful of evaluated tokens.
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	last := ""
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"test"}`)
			return
		case "/api/chat":
		default:
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
			Think    *bool                      `json:"think"`
			Options  map[string]any             `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Think == nil || *req.Think {
			t.Errorf("think must be explicitly false")
		}
		if req.Options["num_ctx"] != float64(16384) {
			t.Errorf("num_ctx = %v, want the fixed 16384 on every call", req.Options["num_ctx"])
		}
		prompt := req.Messages[0].Content
		toks := len(strings.Fields(prompt))
		mu.Lock()
		evaluated := toks
		if prompt == last {
			evaluated = 3
		}
		last = prompt
		mu.Unlock()
		var buf bytes.Buffer
		buf.WriteString(`{"message":{"content":"OK"},"done":false}` + "\n")
		fmt.Fprintf(&buf, `{"message":{"content":""},"done":true,"prompt_eval_count":%d,"prompt_eval_duration":%d,"eval_count":20,"eval_duration":1000000000,"load_duration":5000000}`+"\n",
			evaluated, int64(evaluated)*1_000_000)
		w.Write(buf.Bytes())
	}))
}

func TestRunProducesAllSections(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	res, err := Run(context.Background(), Config{
		BaseURL:      srv.URL,
		Model:        "m",
		AllowedHosts: []string{"127.0.0.1"},
		ContextSizes: []int{512, 1024},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Prefill) != 2 || res.Prefill[1].PromptTokens <= res.Prefill[0].PromptTokens {
		t.Fatalf("prefill curve = %+v, want two growing points", res.Prefill)
	}
	if res.KVCache.WarmEvaluatedToks != 3 || res.KVCache.PrefixReusePercent < 99 {
		t.Fatalf("kv-cache = %+v, want the repeated prompt to reuse the prefix", res.KVCache)
	}
	if len(res.Tasks) != len(representativeTasks) || res.Summary.GenTokPerS != 20 {
		t.Fatalf("tasks = %+v summary = %+v", res.Tasks, res.Summary)
	}
	if res.Schema != SchemaVersion || res.Hardware.NumCPU == 0 {
		t.Fatalf("missing schema/hardware: %+v", res)
	}
}

func TestRunRefusesHostOutsideAllowlist(t *testing.T) {
	_, err := Run(context.Background(), Config{BaseURL: "http://example.com:11434", Model: "m", AllowedHosts: []string{"127.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
		t.Fatalf("expected an allowlist refusal, got %v", err)
	}
}

func TestFillerPromptsDifferInPrefix(t *testing.T) {
	a, b := fillerPrompt(100, "aaaa"), fillerPrompt(100, "bbbb")
	if a[:20] == b[:20] {
		t.Fatal("different nonces must give different prefixes (cold prefill)")
	}
}

func TestCompareFlagsDirection(t *testing.T) {
	a := &Result{Summary: Summary{GenTokPerS: 10, MedianTTFTMs: 1000}}
	b := &Result{Summary: Summary{GenTokPerS: 12, MedianTTFTMs: 800}}
	for _, d := range Compare(a, b) {
		switch d.Metric {
		case "gen_tok_per_s":
			if d.DeltaPct != 20 || !d.HigherIsBetter {
				t.Errorf("gen delta %+v", d)
			}
		case "median_ttft_ms":
			if d.DeltaPct != -20 || d.HigherIsBetter {
				t.Errorf("ttft delta %+v", d)
			}
		}
	}
}
