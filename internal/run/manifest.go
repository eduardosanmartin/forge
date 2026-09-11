// Package run implements the run manifest (RF-11), budget guardrails (RNF-8)
// and sensitivity ceiling enforcement (RNF-9) for autonomous execution.
//
// Manifest is the single entrypoint for autonomy: goal + spec + budgets +
// HITL checkpoints. It is JSON (repo's existing convention) so it validates
// via the same DisallowUnknownFields path as config. YAML front-matter (RUN.md)
// is a follow-up; JSON keeps the slice minimal and testable.
//
// Budgets are hard walls: wall-clock, token, and iteration limits kill the
// run, they do not warn. The git safety floor (RNF-8.2) lives in internal/perms
// and is already wired; this package adds the exhaustion check.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
)

// Mode enumerates autonomy levels (§7.2). dry_run never writes.
const (
	ModeDryRun     = "dry_run"
	ModeSupervised = "supervised"
	ModeCheckpoint = "checkpoint"
	ModeAutonomous = "autonomous"
)

// Manifest is the run manifest (§7.1 minimal slice).
type Manifest struct {
	RunID  string `json:"run_id"`
	Mode   string `json:"mode"`
	Goal   string `json:"goal"`
	Spec   string `json:"spec,omitempty"`
	SpecRef string `json:"spec_ref,omitempty"`

	Budget Budget    `json:"budget"`
	Git    GitConfig `json:"git"`
	HITL   HITLConfig `json:"hitl"`

	// Tasks is the decomposed SPEC. When empty the runner creates a single
	// task from Goal (keeps decomposition LLM-free for determinism). Explicit
	// tasks make the manifest self-contained and trivially testable.
	Tasks []Task `json:"tasks,omitempty"`
}

// Budget bounds unattended execution (RNF-8).
type Budget struct {
	MaxWallClock      string  `json:"max_wall_clock,omitempty"`       // e.g. "30m", "6h", "" = unlimited
	MaxTokens         int     `json:"max_tokens,omitempty"`
	MaxIterations     int     `json:"max_iterations,omitempty"`
	MaxRetriesPerTask int     `json:"max_retries_per_task,omitempty"`
	MaxCostUSD        float64 `json:"max_cost_usd,omitempty"` // reserved, not enforced (needs cost metering)
}

// GitConfig mirrors §7.1 git isolation.
type GitConfig struct {
	Isolation     string `json:"isolation,omitempty"`      // worktree | branch | none
	BaseBranch    string `json:"base_branch,omitempty"`
	WorkBranch    string `json:"work_branch,omitempty"`
	CommitPerTask bool   `json:"commit_per_task,omitempty"`
	MergeToBase   string `json:"merge_to_base,omitempty"` // manual | auto_if_all_hitl_passed
}

// HITLConfig declares human checkpoints.
type HITLConfig struct {
	Checkpoints []Checkpoint `json:"checkpoints,omitempty"`
}

// Checkpoint is one HITL pause point.
type Checkpoint struct {
	ID       string   `json:"id"`
	Trigger  string   `json:"trigger"` // after_spec_decomposition | before_task | after_task | before_merge | budget_threshold | before_editing
	Required bool     `json:"required"`
	Match    []string `json:"match,omitempty"`     // for before_editing
	Threshold float64 `json:"threshold,omitempty"` // for budget_threshold (0,1)
}

// Task is one atomic SPEC unit (RF-11.3).
type Task struct {
	ID           string `json:"id"`
	Goal         string `json:"goal"`
	DoneCriteria string `json:"done_criteria,omitempty"`
}

// Known triggers.
const (
	TriggerAfterDecomposition = "after_spec_decomposition"
	TriggerBeforeTask         = "before_task"
	TriggerAfterTask          = "after_task"
	TriggerBeforeMerge        = "before_merge"
	TriggerBudgetThreshold    = "budget_threshold"
	TriggerBeforeEditing      = "before_editing"
)

// ParseFile reads path as JSON manifest and validates it.
func ParseFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	// Resolve spec_ref relative to manifest file when present and spec empty.
	if strings.TrimSpace(m.Spec) == "" && strings.TrimSpace(m.SpecRef) != "" {
		ref := m.SpecRef
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(filepath.Dir(path), ref)
		}
		specData, err := os.ReadFile(ref)
		if err != nil {
			return nil, fmt.Errorf("read spec_ref %s: %w", ref, err)
		}
		m.Spec = string(specData)
	}
	return m, nil
}

// Parse decodes JSON and validates with unknown-field rejection.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks manifest invariants, not sensitivity ceiling (done via config).
func (m *Manifest) Validate() error {
	var errs []error
	if strings.TrimSpace(m.RunID) == "" {
		errs = append(errs, errors.New("run_id must not be empty"))
	}
	if err := validateRunID(m.RunID); err != nil {
		errs = append(errs, err)
	}
	if rank := config.AutonomyRank(m.Mode); rank < 0 {
		errs = append(errs, fmt.Errorf("mode %q is invalid (allowed: dry_run, supervised, checkpoint, autonomous)", m.Mode))
	}
	if strings.TrimSpace(m.Goal) == "" {
		errs = append(errs, errors.New("goal must not be empty"))
	}
	if strings.TrimSpace(m.Spec) == "" && strings.TrimSpace(m.SpecRef) == "" && len(m.Tasks) == 0 {
		// Goal alone suffices when spec is embedded in goal for the minimal slice,
		// but we still require at least goal; this condition is informational only.
	}
	// Budget
	if m.Budget.MaxWallClock != "" {
		if _, err := time.ParseDuration(m.Budget.MaxWallClock); err != nil {
			errs = append(errs, fmt.Errorf("budget.max_wall_clock %q: %w", m.Budget.MaxWallClock, err))
		}
	}
	if m.Budget.MaxTokens < 0 {
		errs = append(errs, fmt.Errorf("budget.max_tokens must be >= 0"))
	}
	if m.Budget.MaxIterations < 0 {
		errs = append(errs, fmt.Errorf("budget.max_iterations must be >= 0"))
	}
	if m.Budget.MaxRetriesPerTask < 0 {
		errs = append(errs, fmt.Errorf("budget.max_retries_per_task must be >= 0"))
	}
	if m.Budget.MaxCostUSD < 0 {
		errs = append(errs, fmt.Errorf("budget.max_cost_usd must be >= 0"))
	}
	// Git
	if m.Git.Isolation != "" {
		switch m.Git.Isolation {
		case "worktree", "branch", "none":
		default:
			errs = append(errs, fmt.Errorf("git.isolation %q is invalid (allowed: worktree, branch, none)", m.Git.Isolation))
		}
	}
	if m.Git.MergeToBase != "" {
		switch m.Git.MergeToBase {
		case "manual", "auto_if_all_hitl_passed":
		default:
			errs = append(errs, fmt.Errorf("git.merge_to_base %q is invalid (allowed: manual, auto_if_all_hitl_passed)", m.Git.MergeToBase))
		}
	}
	// HITL
	seenID := make(map[string]bool, len(m.HITL.Checkpoints))
	for i, cp := range m.HITL.Checkpoints {
		if strings.TrimSpace(cp.ID) == "" {
			errs = append(errs, fmt.Errorf("hitl.checkpoints[%d].id must not be empty", i))
		} else if seenID[cp.ID] {
			errs = append(errs, fmt.Errorf("hitl.checkpoints[%d].id %q duplicated", i, cp.ID))
		}
		seenID[cp.ID] = true
		if !isKnownTrigger(cp.Trigger) {
			errs = append(errs, fmt.Errorf("hitl.checkpoints[%d].trigger %q is invalid", i, cp.Trigger))
		}
		if cp.Trigger == TriggerBudgetThreshold {
			if cp.Threshold <= 0 || cp.Threshold >= 1 {
				errs = append(errs, fmt.Errorf("hitl.checkpoints[%d].threshold must be in (0,1) for budget_threshold trigger", i))
			}
		}
		if cp.Trigger == TriggerBeforeEditing && len(cp.Match) == 0 {
			errs = append(errs, fmt.Errorf("hitl.checkpoints[%d] with before_editing trigger must have non-empty match", i))
		}
	}
	// Tasks
	seenTaskID := make(map[string]bool, len(m.Tasks))
	for i, t := range m.Tasks {
		if strings.TrimSpace(t.ID) == "" {
			errs = append(errs, fmt.Errorf("tasks[%d].id must not be empty", i))
		} else if seenTaskID[t.ID] {
			errs = append(errs, fmt.Errorf("tasks[%d].id %q duplicated", i, t.ID))
		}
		seenTaskID[t.ID] = true
		if strings.TrimSpace(t.Goal) == "" {
			errs = append(errs, fmt.Errorf("tasks[%d].goal must not be empty", i))
		}
	}
	return errors.Join(errs...)
}

func validateRunID(id string) error {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return fmt.Errorf("run_id %q must not contain path separators or ..", id)
	}
	return nil
}

func isKnownTrigger(t string) bool {
	switch t {
	case TriggerAfterDecomposition, TriggerBeforeTask, TriggerAfterTask, TriggerBeforeMerge, TriggerBudgetThreshold, TriggerBeforeEditing:
		return true
	default:
		return false
	}
}

// HasPreMergeRequired reports whether HITL declares a required before_merge checkpoint.
func (m *Manifest) HasPreMergeRequired() bool {
	for _, cp := range m.HITL.Checkpoints {
		if cp.Trigger == TriggerBeforeMerge && cp.Required {
			return true
		}
	}
	return false
}

// EffectiveTasks returns the task list the runner will execute. When the
// manifest declares no tasks, a single task derived from Goal is returned so
// the manifest stays usable without an LLM decomposer.
func (m *Manifest) EffectiveTasks() []Task {
	if len(m.Tasks) > 0 {
		return m.Tasks
	}
	return []Task{{ID: "task-1", Goal: m.Goal, DoneCriteria: "goal achieved"}}
}

// WallClockDuration parses budget.max_wall_clock, or 0 for unlimited.
func (m *Manifest) WallClockDuration() time.Duration {
	if m.Budget.MaxWallClock == "" {
		return 0
	}
	d, _ := time.ParseDuration(m.Budget.MaxWallClock)
	return d
}

// ValidateAgainstSensitivity enforces RNF-9 ceiling for this manifest vs project config.
func (m *Manifest) ValidateAgainstSensitivity(cfg *config.Config) error {
	return config.ValidateAutonomyAgainstSensitivity(m.Mode, cfg.Project.Sensitivity, m.HasPreMergeRequired())
}

// IsolationRequired reports whether the manifest's mode requires git isolation (§3.6 step 2, RNF-8.1).
func (m *Manifest) IsolationRequired() bool {
	// supervised may run without isolation (human watching); everything else must be isolated.
	return m.Mode != ModeSupervised && m.Mode != ModeDryRun
}
