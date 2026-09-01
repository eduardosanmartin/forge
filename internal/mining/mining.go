// Package mining implements deterministic skill mining from successful session trajectories.
//
// RNF-6.2: the persistent message store is the recording; this package adds the mining pipeline.
// RF-4.3: propose a new skill from repeated successful trajectories.
// RF-4.4: distilled proposals always go through human approval (forge skill install); never auto-enabled.
//
// Clustering uses the same token bag-of-words embedding as internal/skill (hashing trick, 384-dim,
// L2-normalized, cosine threshold default 0.4). Greedy clustering compares each trajectory's embedding
// against existing cluster centroids (average of member embeddings); the first cluster with
// score >= Threshold joins, otherwise a new cluster is created. Clusters with >= MinClusterSize
// members become proposals.
//
// Distillation is deterministic (no LLM): it emits "Recurring workflow (observed N times):" plus
// the common tool sequence and example user prompts.
package mining

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Step is a single tool invocation within a turn.
type Step struct {
	ToolName      string
	ArgsSummary   string
	ResultSummary string
}

// Turn groups one user prompt and its subsequent tool steps.
type Turn struct {
	UserPrompt string
	Steps      []Step
}

// Trajectory is one session's grouped turns.
type Trajectory struct {
	SessionID string
	Turns     []Turn
}

// Options tunes mining.
type Options struct {
	MinClusterSize int     // default 2
	Threshold      float32 // default 0.4
	MaxInstructions int    // max lines in instructions (0 = unlimited)
}

// Proposal is a distilled skill draft.
type Proposal struct {
	Name               string
	Description        string
	Category           string
	ActivationKeywords []string
	Instructions       string
	SourceSessions     []string
	Steps              []string // common tool sequence
}

const dim = 384

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

type trajEmb struct {
	traj Trajectory
	emb  []float32
	text string
}

type cluster struct {
	members  []trajEmb
	centroid []float32
}

// Mine clusters trajectories and returns proposals for clusters meeting thresholds.
func Mine(trajs []Trajectory, opts Options) []Proposal {
	if opts.MinClusterSize == 0 {
		opts.MinClusterSize = 2
	}
	if opts.Threshold == 0 {
		opts.Threshold = 0.4
	}
	if len(trajs) == 0 {
		return nil
	}

	var items []trajEmb
	for _, t := range trajs {
		summary := trajectorySummaryText(t)
		emb := generateEmbedding(summary)
		items = append(items, trajEmb{traj: t, emb: emb, text: summary})
	}

	var clusters []cluster

	for _, it := range items {
		bestIdx := -1
		bestScore := float32(-1)
		for idx, c := range clusters {
			score := cosineSimilarity(it.emb, c.centroid)
			if score >= opts.Threshold && score > bestScore {
				bestScore = score
				bestIdx = idx
			}
		}
		if bestIdx >= 0 {
			clusters[bestIdx].members = append(clusters[bestIdx].members, it)
			// Recompute centroid as average of member embeddings.
			clusters[bestIdx].centroid = averageEmbeddings(clusters[bestIdx].members)
		} else {
			centroid := make([]float32, dim)
			copy(centroid, it.emb)
			clusters = append(clusters, cluster{
				members:  []trajEmb{it},
				centroid: centroid,
			})
		}
	}

	var proposals []Proposal
	for _, c := range clusters {
		if len(c.members) < opts.MinClusterSize {
			continue
		}
		p := distill(c.members, opts)
		proposals = append(proposals, p)
	}

	// Deterministic order by name.
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].Name < proposals[j].Name })
	return proposals
}

func trajectorySummaryText(t Trajectory) string {
	var parts []string
	for _, turn := range t.Turns {
		if turn.UserPrompt != "" {
			parts = append(parts, turn.UserPrompt)
		}
		for _, s := range turn.Steps {
			parts = append(parts, s.ToolName)
			if s.ArgsSummary != "" {
				parts = append(parts, s.ArgsSummary)
			}
		}
	}
	return strings.Join(parts, " ")
}

func averageEmbeddings(members []trajEmb) []float32 {
	if len(members) == 0 {
		return make([]float32, dim)
	}
	avg := make([]float32, dim)
	for _, m := range members {
		for i, v := range m.emb {
			avg[i] += v
		}
	}
	for i := range avg {
		avg[i] /= float32(len(members))
	}
	// L2 normalize.
	var norm float32
	for _, v := range avg {
		norm += v * v
	}
	if norm > 0 {
		norm = float32(math.Sqrt(float64(norm)))
		for i := range avg {
			avg[i] /= norm
		}
	}
	return avg
}

// distill creates a proposal from a cluster.
func distill(members []trajEmb, opts Options) Proposal {
	n := len(members)
	// Collect source sessions.
	sessions := make([]string, 0, n)
	sessionSet := make(map[string]bool)
	for _, m := range members {
		if !sessionSet[m.traj.SessionID] {
			sessionSet[m.traj.SessionID] = true
			sessions = append(sessions, m.traj.SessionID)
		}
	}
	sort.Strings(sessions)

	// Determine common tool sequence: most frequent ordered sequence.
	// Simple: count tool name sequences as strings.
	seqCount := make(map[string]int)
	seqOrder := make(map[string][]string)
	for _, m := range members {
		var seq []string
		for _, turn := range m.traj.Turns {
			for _, s := range turn.Steps {
				seq = append(seq, s.ToolName)
			}
		}
		key := strings.Join(seq, " -> ")
		seqCount[key]++
		if _, ok := seqOrder[key]; !ok {
			seqOrder[key] = seq
		}
	}
	var commonSeq []string
	maxCount := 0
	var commonKey string
	for k, cnt := range seqCount {
		if cnt > maxCount || (cnt == maxCount && k < commonKey) {
			maxCount = cnt
			commonKey = k
			commonSeq = seqOrder[k]
		}
	}
	if len(commonSeq) == 0 {
		commonSeq = []string{"(no tool calls)"}
	}

	// Description: one line from shared steps.
	desc := ""
	if len(commonSeq) > 0 && commonSeq[0] != "(no tool calls)" {
		desc = fmt.Sprintf("Recurring workflow using %s (observed %d times)", strings.Join(commonSeq, ", "), n)
	} else {
		desc = fmt.Sprintf("Recurring workflow (observed %d times)", n)
	}

	// Name slug from most frequent content tokens.
	// Collect tokens from all prompts.
	tokenFreq := make(map[string]int)
	for _, m := range members {
		toks := tokenize(strings.ToLower(m.text))
		for _, tok := range toks {
			if len(tok) < 3 {
				continue
			}
			if isStopword(tok) {
				continue
			}
			tokenFreq[tok]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var sorted []kv
	for k, v := range tokenFreq {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].v == sorted[j].v {
			return sorted[i].k < sorted[j].k
		}
		return sorted[i].v > sorted[j].v
	})
	var topTokens []string
	for _, kv := range sorted {
		if len(topTokens) >= 4 {
			break
		}
		topTokens = append(topTokens, kv.k)
	}
	if len(topTokens) == 0 {
		// Fallback: hash of first session.
		h := sha256.Sum256([]byte(members[0].traj.SessionID))
		topTokens = []string{fmt.Sprintf("workflow-%x", h[:3])}
	}
	slug := strings.Join(topTokens, "-")
	slug = sanitizeSlug(slug)

	// Activation keywords: top tokens (up to 8).
	var keywords []string
	for i, kv := range sorted {
		if i >= 8 {
			break
		}
		keywords = append(keywords, kv.k)
	}
	if len(keywords) == 0 {
		keywords = []string{slug}
	}

	// Instructions deterministic.
	var instr strings.Builder
	instr.WriteString(fmt.Sprintf("Recurring workflow (observed %d times):\n\n", n))
	instr.WriteString("Common tool sequence:\n")
	for i, s := range commonSeq {
		instr.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
	}
	instr.WriteString("\nExample prompts:\n")
	seenPrompts := make(map[string]bool)
	count := 0
	for _, m := range members {
		for _, turn := range m.traj.Turns {
			if turn.UserPrompt == "" {
				continue
			}
			if seenPrompts[turn.UserPrompt] {
				continue
			}
			seenPrompts[turn.UserPrompt] = true
			instr.WriteString(fmt.Sprintf("- %q\n", turn.UserPrompt))
			count++
			if count >= 5 {
				break
			}
		}
		if count >= 5 {
			break
		}
	}
	if opts.MaxInstructions > 0 {
		lines := strings.Split(instr.String(), "\n")
		if len(lines) > opts.MaxInstructions {
			lines = lines[:opts.MaxInstructions]
			instr.Reset()
			instr.WriteString(strings.Join(lines, "\n"))
		}
	}

	stepsCopy := make([]string, len(commonSeq))
	copy(stepsCopy, commonSeq)

	return Proposal{
		Name:               slug,
		Description:        desc,
		Category:           "custom",
		ActivationKeywords: keywords,
		Instructions:       instr.String(),
		SourceSessions:     sessions,
		Steps:              stepsCopy,
	}
}

func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	// Replace non-alnum with hyphen.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	s = b.String()
	// Collapse multiple hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-_")
	if s == "" {
		s = "workflow"
	}
	// Ensure starts with letter.
	if s[0] >= '0' && s[0] <= '9' {
		s = "w-" + s
	}
	// Truncate to 64.
	if len(s) > 64 {
		s = s[:64]
		s = strings.TrimRight(s, "-_")
	}
	// Validate; if invalid, fallback.
	if !nameRe.MatchString(s) {
		// Try to fix: ensure 2 chars min.
		if len(s) == 1 {
			s = s + "0"
		}
		if !nameRe.MatchString(s) {
			s = "workflow-auto"
		}
	}
	return s
}

func tokenize(text string) []string {
	var tokens []string
	var cur strings.Builder
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func isStopword(tok string) bool {
	switch tok {
	case "the", "and", "for", "with", "this", "that", "from", "have", "has", "was", "were", "are", "you", "your", "please", "could", "would", "should":
		return true
	default:
		return false
	}
}

// --- embedding helpers (mirror internal/embedding) ---

func generateEmbedding(text string) []float32 {
	emb := make([]float32, dim)
	lower := strings.ToLower(text)
	tokens := tokenize(lower)
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		h := hashString(tok)
		bucket := int(h % uint32(dim))
		sign := float32(1)
		if (h & 1) == 0 {
			sign = -1
		}
		emb[bucket] += sign
	}
	var norm float32
	for _, v := range emb {
		norm += v * v
	}
	if norm > 0 {
		norm = float32(math.Sqrt(float64(norm)))
		for i := range emb {
			emb[i] /= norm
		}
	}
	return emb
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i]*a[i]
		normB += b[i]*b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
