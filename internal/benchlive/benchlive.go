// Package benchlive is the live performance bench of RNF-10: it measures a
// real local model through Ollama's native API — generation tokens/s,
// time to first token, prefill time per context size, KV-cache prefix
// reuse, and wall-clock time for a small set of representative tasks — and
// writes one JSON result per hardware profile (RNF-10.2: profiles are
// reported separately, never averaged together).
//
// Timings come from the server's own counters (prompt_eval_duration,
// eval_duration), not from a client-side stopwatch, except TTFT and
// wall-clock, which are by definition what the client experiences.
package benchlive

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// SchemaVersion of the result JSON.
const SchemaVersion = 1

// Config drives one bench run.
type Config struct {
	BaseURL string // Ollama base URL, e.g. http://127.0.0.1:11434
	Model   string
	Profile string // "A" (interactive/standard) or "B" (batch/autonomous), spec §5
	// ContextSizes are the approximate prompt sizes (tokens) for the
	// prefill curve. Default 1024, 2048, 4096, 8192.
	ContextSizes []int
	// NumCtx is the context window requested for EVERY call. It is fixed
	// for the whole run on purpose: Ollama reloads the model when num_ctx
	// changes between requests, which would pollute every timing (and
	// defeat the KV-cache measurement). Default 16384.
	NumCtx int
	// NumPredict caps generated tokens for the task set. Default 256.
	NumPredict int
	// AllowedHosts is the network allowlist (RNF-4.9): BaseURL's host must
	// be in it. Empty denies all.
	AllowedHosts []string
	HTTPClient   *http.Client
	// Progress, when set, receives one line per completed measurement.
	Progress func(string)
}

// Result is one bench run.
type Result struct {
	Schema       int            `json:"schema"`
	Profile      string         `json:"profile"`
	Model        string         `json:"model"`
	Backend      string         `json:"backend"`
	StartedAt    time.Time      `json:"started_at"`
	Hardware     Hardware       `json:"hardware"`
	NumCtx       int            `json:"num_ctx"`
	Prefill      []PrefillPoint `json:"prefill"`
	KVCache      KVCache        `json:"kv_cache"`
	Tasks        []TaskResult   `json:"tasks"`
	Summary      Summary        `json:"summary"`
	TotalWallMs  int64          `json:"total_wall_ms"`
	ModelLoadMs  int64          `json:"model_load_ms"`
	BackendBuild string         `json:"backend_version,omitempty"`
}

// Hardware is a best-effort fingerprint of the machine the bench ran on.
type Hardware struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	NumCPU   int    `json:"num_cpu"`
	CPUModel string `json:"cpu_model,omitempty"`
}

// PrefillPoint is the prompt-processing cost at one context size.
type PrefillPoint struct {
	TargetTokens    int     `json:"target_tokens"`
	PromptTokens    int     `json:"prompt_tokens"`
	PrefillMs       float64 `json:"prefill_ms"`
	PrefillTokPerS  float64 `json:"prefill_tok_per_s"`
	TTFTMs          float64 `json:"ttft_ms"`
	ContextFraction float64 `json:"context_fraction"` // PromptTokens / NumCtx
}

// KVCache compares the same prompt sent cold and then again (RNF-2.4:
// a stable prefix lets the server skip re-processing it).
type KVCache struct {
	TargetTokens       int     `json:"target_tokens"`
	PromptTokens       int     `json:"prompt_tokens"`
	ColdPrefillMs      float64 `json:"cold_prefill_ms"`
	WarmPrefillMs      float64 `json:"warm_prefill_ms"`
	WarmEvaluatedToks  int     `json:"warm_evaluated_tokens"`
	ColdTTFTMs         float64 `json:"cold_ttft_ms"`
	WarmTTFTMs         float64 `json:"warm_ttft_ms"`
	TTFTSpeedup        float64 `json:"ttft_speedup"`
	PrefixReusePercent float64 `json:"prefix_reuse_percent"`
}

// TaskResult is one representative task.
type TaskResult struct {
	Name             string  `json:"name"`
	WallMs           float64 `json:"wall_ms"`
	TTFTMs           float64 `json:"ttft_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	GenTokPerS       float64 `json:"gen_tok_per_s"`
}

// Summary holds the headline numbers.
type Summary struct {
	GenTokPerS     float64 `json:"gen_tok_per_s"`     // mean over tasks
	MedianTTFTMs   float64 `json:"median_ttft_ms"`    // over tasks
	PrefillTokPerS float64 `json:"prefill_tok_per_s"` // mean over prefill points
	TasksWallMs    float64 `json:"tasks_wall_ms"`     // sum over tasks
	Prefill4kMs    float64 `json:"prefill_4k_ms"`     // prefill at the ~4k point, if measured
	Prefill8kMs    float64 `json:"prefill_8k_ms"`     // prefill at the ~8k point, if measured
	KVCacheSpeedup float64 `json:"kv_cache_ttft_speedup"`
}

// representativeTasks are short, deterministic-ish coding requests.
var representativeTasks = []struct{ name, prompt string }{
	{"codegen", "Write a Go function `Reverse(s string) string` that reverses a string by runes. Reply with only the code."},
	{"explain", "In at most five sentences, explain what a Go context.Context is used for."},
	{"refactor", "Rewrite this Go loop with strings.Builder and reply with only the code:\nres := \"\"\nfor _, w := range words { res += w + \" \" }"},
}

// Run executes the bench.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Model == "" {
		return nil, errors.New("model is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:11434"
	}
	if err := checkAllowed(cfg.BaseURL, cfg.AllowedHosts); err != nil {
		return nil, err
	}
	if len(cfg.ContextSizes) == 0 {
		cfg.ContextSizes = []int{1024, 2048, 4096, 8192}
	}
	if cfg.NumCtx <= 0 {
		cfg.NumCtx = 16384
	}
	if cfg.NumPredict <= 0 {
		cfg.NumPredict = 256
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Minute}
	}
	if cfg.Profile == "" {
		cfg.Profile = "A"
	}
	progress := cfg.Progress
	if progress == nil {
		progress = func(string) {}
	}
	c := &ollama{base: strings.TrimRight(cfg.BaseURL, "/"), model: cfg.Model, numCtx: cfg.NumCtx, http: cfg.HTTPClient}

	start := time.Now()
	res := &Result{
		Schema:    SchemaVersion,
		Profile:   cfg.Profile,
		Model:     cfg.Model,
		Backend:   "ollama",
		StartedAt: start.UTC(),
		Hardware:  detectHardware(),
		NumCtx:    cfg.NumCtx,
	}
	res.BackendBuild = c.version(ctx)

	// Warm-up: loads the model with the run's fixed num_ctx so no later
	// measurement pays the load.
	warm, err := c.chat(ctx, "Reply with the single word OK.", 4)
	if err != nil {
		return nil, fmt.Errorf("warm-up call: %w", err)
	}
	res.ModelLoadMs = warm.LoadDuration.Milliseconds()
	progress(fmt.Sprintf("warm-up done (model load %d ms)", res.ModelLoadMs))

	// Prefill curve: each prompt starts with a fresh nonce so no cached
	// prefix is reused (cold prefill by construction).
	for _, size := range cfg.ContextSizes {
		if size+64 >= cfg.NumCtx {
			progress(fmt.Sprintf("prefill %d skipped: does not fit num_ctx %d", size, cfg.NumCtx))
			continue
		}
		r, err := c.chat(ctx, fillerPrompt(size, nonce()), 4)
		if err != nil {
			return nil, fmt.Errorf("prefill %d: %w", size, err)
		}
		pt := PrefillPoint{
			TargetTokens:    size,
			PromptTokens:    r.PromptEvalCount,
			PrefillMs:       ms(r.PromptEvalDuration),
			PrefillTokPerS:  perSecond(r.PromptEvalCount, r.PromptEvalDuration),
			TTFTMs:          ms(r.TTFT),
			ContextFraction: float64(r.PromptEvalCount) / float64(cfg.NumCtx),
		}
		res.Prefill = append(res.Prefill, pt)
		progress(fmt.Sprintf("prefill ~%d tok: %d tok in %.0f ms (%.1f tok/s)", size, pt.PromptTokens, pt.PrefillMs, pt.PrefillTokPerS))
	}

	// KV-cache prefix reuse: the same prompt twice, at the largest
	// measured context size up to 4096 tokens (on slow CPUs a 4k cold
	// prefill alone can take minutes).
	kvSize := 0
	for _, size := range cfg.ContextSizes {
		if size <= 4096 && size > kvSize && size+64 < cfg.NumCtx {
			kvSize = size
		}
	}
	kvPrompt := fillerPrompt(kvSize, nonce())
	if kvSize > 0 {
		cold, err := c.chat(ctx, kvPrompt, 4)
		if err != nil {
			return nil, fmt.Errorf("kv-cache cold: %w", err)
		}
		warmKV, err := c.chat(ctx, kvPrompt, 4)
		if err != nil {
			return nil, fmt.Errorf("kv-cache warm: %w", err)
		}
		kv := KVCache{
			TargetTokens:      kvSize,
			PromptTokens:      cold.PromptEvalCount,
			ColdPrefillMs:     ms(cold.PromptEvalDuration),
			WarmPrefillMs:     ms(warmKV.PromptEvalDuration),
			WarmEvaluatedToks: warmKV.PromptEvalCount,
			ColdTTFTMs:        ms(cold.TTFT),
			WarmTTFTMs:        ms(warmKV.TTFT),
		}
		if kv.WarmTTFTMs > 0 {
			kv.TTFTSpeedup = kv.ColdTTFTMs / kv.WarmTTFTMs
		}
		// Some Ollama versions count cached tokens in prompt_eval_count;
		// fall back to the prefill time saved when the count doesn't drop.
		switch {
		case kv.PromptTokens > 0 && kv.WarmEvaluatedToks < kv.PromptTokens:
			kv.PrefixReusePercent = 100 * float64(kv.PromptTokens-kv.WarmEvaluatedToks) / float64(kv.PromptTokens)
		case kv.ColdPrefillMs > 0 && kv.WarmPrefillMs < kv.ColdPrefillMs:
			kv.PrefixReusePercent = 100 * (1 - kv.WarmPrefillMs/kv.ColdPrefillMs)
		}
		res.KVCache = kv
		progress(fmt.Sprintf("kv-cache (~%d tok): TTFT cold %.0f ms -> warm %.0f ms (%.1fx, %.0f%% prefix reused)", kvSize, kv.ColdTTFTMs, kv.WarmTTFTMs, kv.TTFTSpeedup, kv.PrefixReusePercent))
	}

	// Representative tasks.
	for _, task := range representativeTasks {
		t0 := time.Now()
		r, err := c.chat(ctx, task.prompt, cfg.NumPredict)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", task.name, err)
		}
		tr := TaskResult{
			Name:             task.name,
			WallMs:           ms(time.Since(t0)),
			TTFTMs:           ms(r.TTFT),
			PromptTokens:     r.PromptEvalCount,
			CompletionTokens: r.EvalCount,
			GenTokPerS:       perSecond(r.EvalCount, r.EvalDuration),
		}
		res.Tasks = append(res.Tasks, tr)
		progress(fmt.Sprintf("task %s: %.0f ms wall, %d tok at %.1f tok/s", tr.Name, tr.WallMs, tr.CompletionTokens, tr.GenTokPerS))
	}

	res.Summary = summarize(res)
	res.TotalWallMs = time.Since(start).Milliseconds()
	return res, nil
}

func summarize(r *Result) Summary {
	var s Summary
	if n := len(r.Tasks); n > 0 {
		ttfts := make([]float64, 0, n)
		for _, t := range r.Tasks {
			s.GenTokPerS += t.GenTokPerS / float64(n)
			s.TasksWallMs += t.WallMs
			ttfts = append(ttfts, t.TTFTMs)
		}
		s.MedianTTFTMs = median(ttfts)
	}
	if n := len(r.Prefill); n > 0 {
		for _, p := range r.Prefill {
			s.PrefillTokPerS += p.PrefillTokPerS / float64(n)
			switch p.TargetTokens {
			case 4096:
				s.Prefill4kMs = p.PrefillMs
			case 8192:
				s.Prefill8kMs = p.PrefillMs
			}
		}
	}
	s.KVCacheSpeedup = r.KVCache.TTFTSpeedup
	return s
}

// --- Ollama native client ---

type ollama struct {
	base   string
	model  string
	numCtx int
	http   *http.Client
}

type chatResult struct {
	TTFT               time.Duration
	PromptEvalCount    int
	PromptEvalDuration time.Duration
	EvalCount          int
	EvalDuration       time.Duration
	LoadDuration       time.Duration
}

func (o *ollama) version(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+"/api/version", nil)
	if err != nil {
		return ""
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&v)
	return v.Version
}

// chat streams one /api/chat call and returns the server-side counters plus
// the client-observed time to the first content token. "think": false keeps
// reasoning models from spending the token budget on hidden thinking.
func (o *ollama) chat(ctx context.Context, prompt string, numPredict int) (chatResult, error) {
	body, _ := json.Marshal(map[string]any{
		"model":    o.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   true,
		"think":    false,
		"options": map[string]any{
			"num_ctx":     o.numCtx,
			"num_predict": numPredict,
			"temperature": 0,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return chatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := o.http.Do(req)
	if err != nil {
		return chatResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return chatResult{}, fmt.Errorf("ollama %s: %s", resp.Status, strings.TrimSpace(b.String()))
	}

	var out chatResult
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done               bool   `json:"done"`
			Error              string `json:"error"`
			PromptEvalCount    int    `json:"prompt_eval_count"`
			PromptEvalDuration int64  `json:"prompt_eval_duration"`
			EvalCount          int    `json:"eval_count"`
			EvalDuration       int64  `json:"eval_duration"`
			LoadDuration       int64  `json:"load_duration"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return out, fmt.Errorf("ollama: %s", ev.Error)
		}
		if out.TTFT == 0 && ev.Message.Content != "" {
			out.TTFT = time.Since(start)
		}
		if ev.Done {
			out.PromptEvalCount = ev.PromptEvalCount
			out.PromptEvalDuration = time.Duration(ev.PromptEvalDuration)
			out.EvalCount = ev.EvalCount
			out.EvalDuration = time.Duration(ev.EvalDuration)
			out.LoadDuration = time.Duration(ev.LoadDuration)
			if out.TTFT == 0 {
				out.TTFT = time.Since(start)
			}
			return out, nil
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, errors.New("ollama stream ended without a done event")
}

// --- helpers ---

func checkAllowed(base string, allowed []string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid base URL %q", base)
	}
	host := u.Hostname()
	for _, a := range allowed {
		if strings.EqualFold(a, host) || strings.EqualFold(a, u.Host) {
			return nil
		}
		if h, _, err := net.SplitHostPort(a); err == nil && strings.EqualFold(h, host) && strings.EqualFold(a, u.Host) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not in network.allowed_hosts (RNF-4.9)", u.Host)
}

func nonce() string {
	return fmt.Sprintf("%016x", rand.Uint64())
}

// fillerWords build prompts of a predictable size: plain lowercase English
// words tokenize at roughly one token per word on common tokenizers; the
// actual count is read back from the server (prompt_eval_count).
var fillerWords = strings.Fields(`alpha river stone window garden market silver paper engine forest
yellow bridge cloud orange signal planet castle letter winter summer
copper island valley rocket button pencil candle mirror ladder anchor`)

// fillerPrompt returns a prompt of about tokens tokens that starts with a
// unique nonce, so its prefix matches nothing cached before it.
func fillerPrompt(tokens int, nonce string) string {
	var b strings.Builder
	b.WriteString("Session ")
	b.WriteString(nonce)
	b.WriteString(". Ignore the following list and reply with the single word OK.\n")
	r := rand.New(rand.NewSource(int64(len(nonce)) + int64(tokens)))
	for i := 0; i < tokens; i++ {
		b.WriteString(fillerWords[r.Intn(len(fillerWords))])
		if i%16 == 15 {
			b.WriteByte('\n')
		} else {
			b.WriteByte(' ')
		}
	}
	b.WriteString("\nReply with the single word OK.")
	return b.String()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func perSecond(count int, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(count) / d.Seconds()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

func detectHardware() Hardware {
	h := Hardware{OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU()}
	switch runtime.GOOS {
	case "windows":
		h.CPUModel = os.Getenv("PROCESSOR_IDENTIFIER")
	case "linux":
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					if _, v, ok := strings.Cut(line, ":"); ok {
						h.CPUModel = strings.TrimSpace(v)
						break
					}
				}
			}
		}
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			h.CPUModel = strings.TrimSpace(string(out))
		}
	}
	return h
}
