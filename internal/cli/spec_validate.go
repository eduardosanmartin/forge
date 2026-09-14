package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// forge spec validate (RF-8.3) surfaces MECHANICAL divergence signals between
// the spec and the codebase: requirements with zero code references, code
// references pointing at IDs that no longer exist in the spec, and spec
// mutations since the previous validation. Trust boundary notes:
//   - It never mutates the repo (no git tags, no commits); the only write is
//     the .forge/spec-state.json marker in the workspace root.
//   - It is an inventory report, not a gate: a run with signals still exits 0.
// Semantic judgment ("is requirement X actually implemented?") is an LLM
// concern and is explicitly out of scope here; see the footer notes.

// ---- Spec requirement parser ----

// SpecRequirement is one requirement bullet recognized in the spec markdown.
type SpecRequirement struct {
	ID      string
	Section string
	Text    string
}

var (
	// requirementHeadingRe matches group headings like "### RF-1. Núcleo de
	// ejecución de agentes" or "### RNF-2. Eficiencia (diferenciador)".
	requirementHeadingRe = regexp.MustCompile(`^### (RF|RNF)-\d+\.`)
	// requirementIDRe matches the ID at the start of a cleaned bullet:
	// RF-1.1, RNF-4.12, RF-4.1.1, ... Sub-numbering is allowed.
	requirementIDRe = regexp.MustCompile(`^(RF|RNF)-\d+(?:\.\d+)*`)
	// evidenceIDRe matches RFID tokens anywhere in a line of Go code.
	evidenceIDRe = regexp.MustCompile(`\b(RF|RNF)-\d+(?:\.\d+)*\b`)
	// bulletRe matches a markdown list item; the capture keeps the text after
	// the "- " marker (leading indentation means a nested sub-bullet).
	bulletRe = regexp.MustCompile(`^-\s+(.*)$`)
)

// parseSpecRequirements scans spec markdown and returns the ordered list of
// requirements. Parsing rules, tuned against the real spec fixture:
//   - A "### RF-N." / "### RNF-N." heading opens a requirement GROUP; its
//     title is recorded as the parent section for the bullets below it.
//   - Any other heading (including narrative ones like "### 3.6 Title ...")
//     closes the group context: bullets under it are narrative prose and are
//     NOT requirements, even if they mention RF-/RNF- IDs.
//   - Only top-level bullets ("- ...") are candidates; nested sub-bullets are
//     part of the parent requirement text structure and never state an ID.
//   - A bullet is a requirement when the text right after the marker starts
//     with an RF/RNF ID. Leading markdown bold markers are stripped, so both
//     "- RF-4.1 text" and "- **RF-4.1.1 text.**" shapes parse identically.
//   - Fenced code blocks are skipped entirely: they can contain lines that
//     look like headings or references (e.g. TOML run-manifest examples) but
//     belong to literal content, not to the requirement structure.
func ParseSpecRequirements(content string) []SpecRequirement {
	reqs := []SpecRequirement{}
	section := ""
	inFence := false

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			if requirementHeadingRe.MatchString(trimmed) {
				section = strings.TrimSpace(strings.TrimPrefix(trimmed, "###"))
			} else {
				// Any other heading level drops the group context.
				section = ""
			}
			continue
		}

		if section == "" {
			continue
		}
		if line == "" || line[0] == '\t' || (line[0] == ' ') {
			continue
		}
		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[1])
		id, cleaned, ok := parseRequirementID(text)
		if !ok {
			continue
		}
		reqs = append(reqs, SpecRequirement{ID: id, Section: section, Text: cleaned})
	}
	return reqs
}

// parseRequirementID extracts the leading RF/RNF ID from a bullet text and
// returns the cleaned requirement text with bold markers stripped.
func parseRequirementID(text string) (id, cleaned string, ok bool) {
	candidates := []string{text}
	if strings.HasPrefix(text, "**") {
		candidates = append(candidates, strings.TrimSpace(strings.TrimPrefix(text, "**")))
	}
	for _, cand := range candidates {
		if loc := requirementIDRe.FindString(cand); loc != "" {
			cleaned = strings.TrimSpace(strings.TrimSuffix(cand, "**"))
			return loc, cleaned, true
		}
	}
	return "", "", false
}

// ---- Evidence scan ----

// SpecEvidence is one RF/RNF ID mention found in a Go source file of the
// workspace. Test files count as claims like any other file.
type SpecEvidence struct {
	File string `json:"file"`
	Line int    `json:"line"`
	ID   string `json:"id"`
}

var evidenceScanSkipDirs = map[string]bool{
	".git": true, ".forge": true,
	"forge-plugins": true, "vendor": true, "node_modules": true,
}

// scanSpecEvidence walks root collecting every RF/RNF ID mentioned in .go
// files. Skipped directories: .git, forge-plugins, vendor, .forge,
// node_modules. Returns refs ordered by file, then line.
func scanSpecEvidence(root string) ([]SpecEvidence, error) {
	var refs []SpecEvidence
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if root != path && evidenceScanSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(data), "\n") {
			for _, loc := range evidenceIDRe.FindAllString(line, -1) {
				refs = append(refs, SpecEvidence{File: rel, Line: i + 1, ID: loc})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan evidence in %s: %w", root, err)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		if refs[i].Line != refs[j].Line {
			return refs[i].Line < refs[j].Line
		}
		return refs[i].ID < refs[j].ID
	})
	return refs, nil
}

// ---- State marker (RF-8.3) ----

// SpecValidateState is the marker persisted at .forge/spec-state.json after
// every successful validate run. It records which spec content the last
// validation saw, so the next run can report drift.
type SpecValidateState struct {
	SpecSHA256       string `json:"spec_sha256"`
	ValidatedAt      string `json:"validated_at"`
	RequirementCount int    `json:"requirement_count"`
}

func specStatePath(root string) string { return filepath.Join(root, ".forge", "spec-state.json") }

// readSpecState loads the stored marker. A missing file reports "first
// validation" instead of failing the run.
func readSpecState(root string) (*SpecValidateState, error) {
	data, err := os.ReadFile(specStatePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read spec state marker: %w", err)
	}
	var st SpecValidateState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse spec state marker: %w", err)
	}
	return &st, nil
}

func writeSpecState(root string, st SpecValidateState) error {
	if err := os.MkdirAll(filepath.Dir(specStatePath(root)), 0o755); err != nil {
		return fmt.Errorf("create .forge directory: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(specStatePath(root), append(data, '\n'), 0o644)
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ---- Report ----

// SpecSignal is one divergence signal in the validate report.
type SpecSignal struct {
	Kind         string            `json:"kind"`
	Count        int               `json:"count"`
	ChangeState  string            `json:"change_state,omitempty"`
	Requirements []SpecRequirement `json:"requirements,omitempty"`
	Evidence     []SpecEvidence    `json:"evidence,omitempty"`
}

// SpecValidateReport is the full structured result of a validate run.
// Signals are strictly mechanical: presence/absence of IDs on both sides plus
// spec-content drift. Semantic audit (does the code actually do what the
// requirement says) is a documented follow-up, out of scope by design.
type SpecValidateReport struct {
	Path       string `json:"path"`
	SpecSHA256 string `json:"spec_sha256"`
	// ChangeState explains spec drift vs the stored marker: "first validation",
	// "unchanged", or "changed-since-last-validate".
	ChangeState         string            `json:"change_state"`
	LastValidatedAt     string            `json:"last_validated_at,omitempty"`
	RequirementCount    int               `json:"requirement_count"`
	EvidenceCount       int               `json:"evidence_count"`
	ReqsWithoutEvidence []SpecRequirement `json:"req_without_evidence"`
	EvidenceWithoutReq  []SpecEvidence    `json:"evidence_without_req"`
	SemanticAuditNote   string            `json:"semantic_audit_note"`
}

const semanticAuditNote = "Mechanical signals only: this report does not judge whether a requirement is actually implemented (semantic audit). That LLM judgment pass is a documented follow-up, out of scope for `forge spec validate`."

// buildSpecValidateReport runs the three-signal analysis for one spec content
// against the workspace root and persists the state marker. It is exported
// for testing and reuse.
func buildSpecValidateReport(root, specPath, content string) (*SpecValidateReport, error) {
	reqs := ParseSpecRequirements(content)
	evidence, err := scanSpecEvidence(root)
	if err != nil {
		return nil, err
	}

	previous, err := readSpecState(root)
	if err != nil {
		return nil, err
	}
	sha := sha256Hex(content)
	changeState := "unchanged"
	var lastValidated string
	if previous == nil {
		changeState = "first validation"
	} else {
		lastValidated = previous.ValidatedAt
		if previous.SpecSHA256 != sha {
			changeState = "changed-since-last-validate"
		}
	}

	// Requirements seen in the spec (deduped by ID, first occurrence wins).
	seen := map[string]SpecRequirement{}
	for _, r := range reqs {
		if _, dup := seen[r.ID]; !dup {
			seen[r.ID] = r
		}
	}
	// Distinct evidence IDs.
	claimed := map[string][]SpecEvidence{}
	for _, e := range evidence {
		claimed[e.ID] = append(claimed[e.ID], e)
	}

	missing := []SpecRequirement{}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		if len(claimed[id]) == 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return compareSpecID(ids[i], ids[j]) < 0 })
	for _, id := range ids {
		missing = append(missing, seen[id])
	}

	orphans := []SpecEvidence{}
	for id, refs := range claimed {
		if _, known := seen[id]; known {
			continue
		}
		orphans = append(orphans, refs...)
	}

	dedupOrphans := orphans[:0]
	seenRef := map[string]bool{}
	for _, o := range orphans {
		key := fmt.Sprintf("%s:%d:%s", o.File, o.Line, o.ID)
		if !seenRef[key] {
			seenRef[key] = true
			dedupOrphans = append(dedupOrphans, o)
		}
	}

	report := &SpecValidateReport{
		Path:                specPath,
		SpecSHA256:          sha,
		ChangeState:         changeState,
		LastValidatedAt:     lastValidated,
		RequirementCount:    len(seen),
		EvidenceCount:       len(evidence),
		ReqsWithoutEvidence: missing,
		EvidenceWithoutReq:  dedupOrphans,
		SemanticAuditNote:   semanticAuditNote,
	}

	marker := SpecValidateState{
		SpecSHA256:       sha,
		ValidatedAt:      time.Now().UTC().Format(time.RFC3339),
		RequirementCount: len(seen),
	}
	if err := writeSpecState(root, marker); err != nil {
		return nil, err
	}
	return report, nil
}

// compareSpecID gives a deterministic report order: lexicographic on the
// whole ID keeps grouping stable (RF-1.x clusters together) across runs.
func compareSpecID(a, b string) int { return strings.Compare(a, b) }

// ---- Command wiring ----

func newSpecValidateCommand() *cobra.Command {
	var jsonOut bool
	var path string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Mechanical divergence signals between spec and codebase (RF-8.3)",
		Long: "Compare the current spec against the codebase and report mechanical divergence signals:\n" +
			"  - req-without-evidence: spec requirements never mentioned in any .go file.\n" +
			"  - evidence-without-req: RF/RNF IDs referenced in code that no longer exist in the spec.\n" +
			"  - spec-changed-since-last-validate: spec content hash drifted from the stored marker.\n" +
			"This is an inventory report, not a gate: the command always exits 0 when the spec\n" +
			"is readable, regardless of the signals. Semantic judgment (\"is requirement X actually\n" +
			"implemented?\") is explicitly OUT OF SCOPE and a documented follow-up.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecValidate(cmd, cmd.OutOrStdout(), path, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&path, "path", "", "spec file path (default: project.spec_path config, then probe)")
	return cmd
}

func runSpecValidate(cmd *cobra.Command, out io.Writer, flagPath string, jsonOut bool) error {
	const command = "spec validate"
	root, cfgSpecPath, err := specInvocation(cmd)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	specPath, err := ResolveSpecPath(root, flagPath, cfgSpecPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	content, err := runSpecFileShow(root, specPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	report, err := buildSpecValidateReport(root, specPath, content)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, command, report)
	}
	writeSpecValidateHuman(out, report)
	return nil
}

func writeSpecValidateHuman(out io.Writer, r *SpecValidateReport) {
	fmt.Fprintf(out, "spec validate: %s\n", r.Path)
	fmt.Fprintf(out, "spec sha256: %s\n", r.SpecSHA256)
	fmt.Fprintf(out, "spec state: %s\n", r.ChangeState)
	if r.LastValidatedAt != "" {
		fmt.Fprintf(out, "last validated: %s\n", r.LastValidatedAt)
	}
	fmt.Fprintf(out, "requirements: %d | evidence refs: %d\n\n", r.RequirementCount, r.EvidenceCount)

	fmt.Fprintf(out, "req-without-evidence: %d\n", len(r.ReqsWithoutEvidence))
	for _, req := range r.ReqsWithoutEvidence {
		fmt.Fprintf(out, "  %s (%s)\n", req.ID, req.Section)
	}

	fmt.Fprintf(out, "\nevidence-without-req: %d\n", len(r.EvidenceWithoutReq))
	for _, e := range r.EvidenceWithoutReq {
		fmt.Fprintf(out, "  %s:%d (%s)\n", e.File, e.Line, e.ID)
	}

	fmt.Fprintf(out, "\nspec state signal: %s\n", r.ChangeState)
	fmt.Fprintln(out)
	fmt.Fprintln(out, r.SemanticAuditNote)
}
