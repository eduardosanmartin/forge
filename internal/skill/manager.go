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

	"github.com/eduardosanmartin/forge/internal/approval"
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

// SkillInfo describes a loaded skill for Info() / RPC listing.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	Origin      string `json:"origin"`
	Enabled     bool   `json:"enabled"`
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
	root            string // remembered project root for Reload()
	globalRoot      string // remembered global root for Reload(); empty if Scan (not ScanAll) was used
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

// StripChecksumLine is exported for CLI install to compute the approval hash consistently.
// It removes the line containing "checksum:" from data for hashing.
// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
func StripChecksumLine(data []byte) []byte {
	return stripChecksumLine(data)
}

// isApprovedData verifies that dir/approved.flag contains a valid approval
// record matching data (SKILL.md bytes) minus the checksum line.
// Supports v1 and v2, with v2 without anchor falling back to hash check.
func isApprovedData(dir string, data []byte) bool {
	cleaned := stripChecksumLine(data)
	sum := sha256.Sum256(cleaned)
	expected := "sha256:" + hex.EncodeToString(sum[:])
	// Try strict name binding if we can parse skill name from data
	name := ""
	if sk, err := parseSkillFile(data, dir); err == nil {
		name = sk.Name
	} else {
		name = filepath.Base(dir)
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	err := approval.VerifyFile(dir, "skill", name, expected, slog.Default())
	if err == nil {
		return true
	}
	// Fallback for legacy isApprovedData callers without name knowledge:
	// if VerifyFile failed due to name mismatch but hash matches, check raw.
	if extracted := approval.ExtractSHA256(dir); strings.EqualFold(strings.TrimSpace(extracted), strings.TrimSpace(expected)) {
		// Need to verify signature if anchor present; without it fallback is hash.
		data2, _ := os.ReadFile(filepath.Join(dir, "approved.flag"))
		if approval.IsV2(data2) {
			if rec, err := approval.ParseV2(data2); err == nil {
				anchor, hasAnchor, _ := approval.LoadAnchor()
				if !hasAnchor {
					return strings.EqualFold(strings.TrimSpace(rec.SHA256), strings.TrimSpace(expected))
				}
				if approval.Verify(rec, expected, anchor) == nil {
					return true
				}
				return false
			}
		} else {
			// v1 hash match already handled below, but return true here
			return true
		}
	}
	// Also handle v1 case
	if isSkillV1Match(dir, expected) {
		return true
	}
	return false
}

func isSkillV1Match(dir, expected string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "approved.flag"))
	if err != nil {
		return false
	}
	if approval.IsV2(data) {
		return false
	}
	trimmed := strings.TrimSpace(string(data))
	return strings.EqualFold(trimmed, strings.TrimSpace(expected))
}

// isApproved verifies that dir/approved.flag contains "sha256:<hex>" matching
// the hash of the current SKILL.md bytes minus the checksum line.
// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
func isApproved(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	return isApprovedData(dir, data)
}

// Scan discovers <root>/<name>/SKILL.md, parses and validates each skill.
// Local skills are auto-enabled; external skills are loaded but remain disabled
// unless ApproveExternal is true (or per-skill approved.flag exists) and Enable is called explicitly.
// Missing root directory is NOT an error (zero skills is valid).
// It returns per-skill LoadResults and an aggregated error if any skill failed.
// Every skill scanned this way has Origin == OriginProject; use ScanAll to
// also load a global root (see config.GlobalSkillsDir).
func (m *Manager) Scan(root string) ([]LoadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.root = root
	m.globalRoot = ""
	m.resetStateLocked()
	return m.scanRootLocked(root, OriginProject)
}

// ScanAll is Scan plus a second root loaded as OriginGlobal — every skill
// found under globalRoot regardless of project root. A skill present in
// both roots with the same name resolves to the project's copy: the global
// entry is skipped entirely (not an error, not a LoadResult) rather than
// overwriting it, since project scope is more specific. Either root missing
// is not an error, same as Scan.
func (m *Manager) ScanAll(projectRoot, globalRoot string) ([]LoadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.root = projectRoot
	m.globalRoot = globalRoot
	m.resetStateLocked()

	results, projErr := m.scanRootLocked(projectRoot, OriginProject)
	globalResults, globalErr := m.scanRootLocked(globalRoot, OriginGlobal)
	results = append(results, globalResults...)

	switch {
	case projErr != nil && globalErr != nil:
		return results, fmt.Errorf("%v; %v", projErr, globalErr)
	case projErr != nil:
		return results, projErr
	default:
		return results, globalErr
	}
}

// resetStateLocked clears all loaded/enabled skills and the embedding
// store. Caller holds m.mu. Shared by Scan and ScanAll so both start from
// the same clean slate regardless of how many roots follow.
func (m *Manager) resetStateLocked() {
	m.skills = make(map[string]*Skill)
	m.enabled = make(map[string]bool)
	// Reset embedding store by creating a fresh one (in-memory, no persistent state to clear otherwise).
	// The store has no Clear API, so recreate.
	if m.embedStore != nil {
		_ = m.embedStore.Close()
	}
	m.embedStore, _ = embedding.NewStore("")
}

// scanRootLocked loads every <root>/<name>/SKILL.md under root, tagging
// each loaded skill with origin. Caller holds m.mu and has already reset
// state if this is the first root of a fresh scan. A name already present
// in m.skills (from an earlier root in the same scan) is skipped silently
// — see ScanAll's doc comment on precedence.
func (m *Manager) scanRootLocked(root string, origin Origin) ([]LoadResult, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skill root %q: %w", root, err)
	}

	var results []LoadResult
	var errs []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, exists := m.skills[e.Name()]; exists {
			continue // already loaded from a higher-precedence root
		}
		skillDir := filepath.Join(root, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue // not a skill directory
		}
		res := LoadResult{Name: e.Name()}
		if err := m.loadOneLocked(skillDir, skillFile, origin); err != nil {
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
func (m *Manager) loadOneLocked(skillDir, skillFile string, origin Origin) error {
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
	// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
	if sk.Source == SourceExternal {
		if !m.approveExternal {
			if err := m.checkSkillApproval(skillDir, data, sk.Name); err != nil {
				return fmt.Errorf("%w: external skill %q requires explicit approval (%v; re-run 'forge skill install' or start serve with --approve-external-plugins)", ErrApprovalRequired, sk.Name, err)
			}
		}
		if err := verifySkillChecksum(data, sk.Checksum); err != nil {
			return err
		}
	}

	// Insert.
	sk.Origin = origin
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

func (m *Manager) checkSkillApproval(skillDir string, data []byte, name string) error {
	cleaned := stripChecksumLine(data)
	sum := sha256.Sum256(cleaned)
	expected := "sha256:" + hex.EncodeToString(sum[:])
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	return approval.VerifyFile(skillDir, "skill", name, expected, logger)
}

func (m *Manager) checkSkillApprovalByName(sk *Skill) error {
	data, err := os.ReadFile(filepath.Join(sk.DirPath, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}
	return m.checkSkillApproval(sk.DirPath, data, sk.Name)
}

func (m *Manager) indexSkillLocked(sk *Skill) {
	// No-op: embeddings are pure functions of text and Relevant() regenerates
	// per call via GenerateEmbedding on query + description+keywords. The
	// previous Store() writes were dead code (never read back). Kept as a
	// hook for future persistent backends; current in-memory store is unused.
	_ = sk
}

// Enable registers the skill as enabled. The skill must have been loaded via Scan.
// For external skills it requires either Options.ApproveExternal or an approved.flag file.
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
	if sk.Source == SourceExternal {
		if !m.approveExternal {
			if err := m.checkSkillApprovalByName(sk); err != nil {
				return fmt.Errorf("%w: external skill %q requires explicit approval (%v; re-run 'forge skill install' or start serve with --approve-external-plugins)", ErrApprovalRequired, name, err)
			}
		}
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

// Info returns sorted SkillInfo for every loaded skill.
func (m *Manager) Info() []SkillInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SkillInfo, 0, len(m.skills))
	for name, sk := range m.skills {
		out = append(out, SkillInfo{
			Name:        name,
			Description: sk.Description,
			Category:    sk.Category,
			Source:      string(sk.Source),
			Origin:      string(sk.Origin),
			Enabled:     m.enabled[name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reload re-scans the remembered root(s), discarding previous state, and
// returns LoadResults. Uses ScanAll if the last scan included a global
// root, Scan otherwise — mirrors whichever of the two the caller used.
func (m *Manager) Reload() ([]LoadResult, error) {
	m.mu.Lock()
	root := m.root
	globalRoot := m.globalRoot
	m.mu.Unlock()
	if root == "" {
		return nil, nil
	}
	if globalRoot != "" {
		return m.ScanAll(root, globalRoot)
	}
	return m.Scan(root)
}

// ActiveManual returns the skills that are active under manual activation
// (SkillsConfig.LazyLoad == false): every loaded skill whose Origin is
// OriginGlobal, plus every loaded skill whose name appears in configEnabled
// (the project's own skills.enabled config list). No embedding, no scoring
// — this never calls Relevant() or touches the embedding store. Unknown
// names in configEnabled (no matching loaded skill) are silently ignored;
// callers that want to validate a config's skill names should cross-check
// against Loaded() themselves.
func (m *Manager) ActiveManual(configEnabled []string) []Skill {
	m.mu.Lock()
	defer m.mu.Unlock()

	want := make(map[string]bool, len(configEnabled))
	for _, n := range configEnabled {
		want[n] = true
	}

	var out []Skill
	for name, sk := range m.skills {
		if sk.Origin == OriginGlobal || want[name] {
			out = append(out, *sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
