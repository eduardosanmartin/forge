package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

// Options configures a Manager.
//
// MinScore is the cosine similarity threshold for Relevant().
// Default 0.4 validated with token-based bag-of-words embeddings (hashing
// trick): identical texts score 1.0, paraphrases sharing 3-4 content words
// (e.g. skill description "Provides guidance for code reviews and pull
// request style checks code review style PR" vs query "Please review this
// code for style issues and PR feedback") score ~0.45-0.60 (combined vs
// paraphrase ~0.61), while genuinely unrelated pairs (no shared content
// words) score ~0.0, with random bucket collisions up to ~0.36 (measured
// "Advice for gardening and cooking recipes" vs "quantum entanglement
// photon galaxy astronomy" = 0.36) and stopword-only overlap ~0.24. 0.4
// cleanly separates paraphrases from unrelated/collisions. With future
// real model embeddings, tune toward semantic recall.
//
// TopK caps how many skills are injected per turn (default 1).
type Options struct {
	// ApproveExternal must be true to load any skill whose frontmatter source is "external".
	ApproveExternal bool
	// MinScore is the relevance threshold for Relevant(). Default 0.4.
	MinScore float32
	// TopK is the max skills injected per turn. Default 1.
	TopK int
	// Logger receives debug messages.
	Logger *slog.Logger
}

// LoadResult reports the outcome of loading one skill directory.
type LoadResult struct {
	Name   string
	Loaded bool
	Err    error
}

// Manager loads, enables, and disables skills with semantic lazy-load.
type Manager struct {
	mu              sync.Mutex
	opts            Options
	skills          map[string]*Skill
	enabled         map[string]bool
	embedStore      *embedding.Store
	logger          *slog.Logger
	approveExternal bool
	minScore        float32
	topK            int
	closed          bool
}

// NewManager creates a Manager.
func NewManager(opts Options) *Manager {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	minScore := opts.MinScore
	if minScore == 0 {
		minScore = 0.4
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = 1
	}
	// Manager owns its own embedding store (do not share v1 retriever's store).
	st, _ := embedding.NewStore("")
	return &Manager{
		opts:            opts,
		skills:          make(map[string]*Skill),
		enabled:         make(map[string]bool),
		embedStore:      st,
		logger:          logger,
		approveExternal: opts.ApproveExternal,
		minScore:        minScore,
		topK:            topK,
	}
}

// Scan discovers <root>/<name>/SKILL.md, parses and validates each skill.
// Local skills are auto-enabled; external skills are loaded but remain disabled
// unless ApproveExternal is true and Enable is called explicitly.
// Missing root directory is NOT an error (zero skills is valid).
// It returns per-skill LoadResults and an aggregated error if any skill failed.
func (m *Manager) Scan(root string) ([]LoadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skill root %q: %w", root, err)
	}

	// Reset state for fresh scan (idempotent re-scan).
	// Clear previous skills and enabled.
	m.skills = make(map[string]*Skill)
	m.enabled = make(map[string]bool)
	// Reset embedding store by creating a fresh one (in-memory, no persistent state to clear otherwise).
	// The store has no Clear API, so recreate.
	if m.embedStore != nil {
		_ = m.embedStore.Close()
	}
	m.embedStore, _ = embedding.NewStore("")

	var results []LoadResult
	var errs []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(root, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue // not a skill directory
		}
		res := LoadResult{Name: e.Name()}
		if err := m.loadOneLocked(skillDir, skillFile); err != nil {
			res.Err = err
			res.Loaded = false
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
		} else {
			res.Loaded = true
		}
		results = append(results, res)
	}

	if len(errs) > 0 {
		return results, fmt.Errorf("skill load failures: %s", strings.Join(errs, "; "))
	}
	return results, nil
}

// loadOneLocked loads a single skill; caller holds m.mu.
func (m *Manager) loadOneLocked(skillDir, skillFile string) error {
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}

	sk, err := parseSkillFile(data, skillDir)
	if err != nil {
		return fmt.Errorf("parse SKILL.md: %w", err)
	}

	if err := validateSkill(&sk); err != nil {
		return fmt.Errorf("validate skill: %w", err)
	}

	// Checksum verification for external before any enable (RNF-4.6).
	if sk.Source == SourceExternal {
		if !m.approveExternal {
			return fmt.Errorf("%w: external skill %q requires explicit approval", ErrApprovalRequired, sk.Name)
		}
		if err := verifySkillChecksum(data, sk.Checksum); err != nil {
			return err
		}
	}

	// Insert.
	m.skills[sk.Name] = &sk

	// Local auto-enabled; external stays disabled until Enable().
	if sk.Source == SourceLocal {
		m.enabled[sk.Name] = true
		// Index embedding for enabled skill.
		m.indexSkillLocked(&sk)
	} else {
		// External: loaded but not enabled.
	}

	return nil
}

func verifySkillChecksum(data []byte, checksum string) error {
	if !strings.HasPrefix(checksum, "sha256:") {
		return fmt.Errorf("%w: checksum %q must have sha256: prefix", ErrChecksumMismatch, checksum)
	}
	want := strings.TrimPrefix(checksum, "sha256:")
	// For skills, the checksum is computed over the file contents WITHOUT the
	// checksum line itself (to avoid self-referential hash); this mirrors the
	// intent of plugin checksum (which hashes the external artifact, not the
	// manifest containing the checksum).
	cleaned := stripChecksumLine(data)
	sum := sha256.Sum256(cleaned)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%w: want %s got %s", ErrChecksumMismatch, want, got)
	}
	return nil
}

// stripChecksumLine removes the line containing "checksum:" from data for hashing.
func stripChecksumLine(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "checksum:") {
			continue
		}
		kept = append(kept, l)
	}
	return []byte(strings.Join(kept, "\n"))
}

func (m *Manager) indexSkillLocked(sk *Skill) {
	combined := sk.Description
	if len(sk.ActivationKeywords) > 0 {
		combined += " " + strings.Join(sk.ActivationKeywords, " ")
	}
	emb, _ := m.embedStore.GenerateEmbedding(combined)
	_, _ = m.embedStore.Store(combined, emb)
	// Also keep implicit map via store; manual cosine will regenerate.
}

// Enable registers the skill as enabled. The skill must have been loaded via Scan.
func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sk, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotLoaded, name)
	}
	if m.enabled[name] {
		return fmt.Errorf("%w: %q", ErrAlreadyEnabled, name)
	}
	m.enabled[name] = true
	m.indexSkillLocked(sk)
	return nil
}

// Disable unregisters the skill.
func (m *Manager) Disable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[name]; !ok {
		return fmt.Errorf("%w: %q", ErrNotLoaded, name)
	}
	if !m.enabled[name] {
		return fmt.Errorf("%w: %q", ErrNotEnabled, name)
	}
	delete(m.enabled, name)
	return nil
}

// Loaded returns the names of all successfully loaded skills, sorted.
func (m *Manager) Loaded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.skills))
	for n := range m.skills {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Enabled returns the names of currently enabled skills, sorted.
func (m *Manager) Enabled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.enabled))
	for n := range m.enabled {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Relevant returns enabled skills whose description+keywords embedding is
// semantically similar to query (cosine >= MinScore), highest first, capped by TopK.
// It uses the manager's internal embedding store (hash-based deterministic).
func (m *Manager) Relevant(query string) ([]Skill, error) {
	if query == "" {
		return nil, nil
	}
	m.mu.Lock()
	// Copy enabled skills to avoid holding lock during embedding generation.
	enabledNames := make([]string, 0, len(m.enabled))
	for n := range m.enabled {
		enabledNames = append(enabledNames, n)
	}
	skillsCopy := make(map[string]*Skill, len(m.skills))
	for k, v := range m.skills {
		skillsCopy[k] = v
	}
	minScore := m.minScore
	topK := m.topK
	store := m.embedStore
	m.mu.Unlock()

	if store == nil {
		return nil, nil
	}
	queryEmb, err := store.GenerateEmbedding(query)
	if err != nil {
		return nil, err
	}

	type scored struct {
		skill Skill
		score float32
	}
	var scoredList []scored
	for _, name := range enabledNames {
		sk, ok := skillsCopy[name]
		if !ok {
			continue
		}
		combined := sk.Description
		if len(sk.ActivationKeywords) > 0 {
			combined += " " + strings.Join(sk.ActivationKeywords, " ")
		}
		skillEmb, err := store.GenerateEmbedding(combined)
		if err != nil {
			continue
		}
		score := cosineSimilarity(queryEmb, skillEmb)
		if score >= minScore {
			scoredList = append(scoredList, scored{skill: *sk, score: score})
		}
	}

	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})

	if len(scoredList) > topK {
		scoredList = scoredList[:topK]
	}

	out := make([]Skill, len(scoredList))
	for i, s := range scoredList {
		out[i] = s.skill
	}
	return out, nil
}

// Close closes the manager. It is idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	m.skills = make(map[string]*Skill)
	m.enabled = make(map[string]bool)
	if m.embedStore != nil {
		_ = m.embedStore.Close()
	}
	return nil
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
