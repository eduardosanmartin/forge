package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
)

func TestParseValidManifest(t *testing.T) {
	data := `{
		"run_id": "demo",
		"mode": "checkpoint",
		"goal": "implement foo",
		"spec": "SPEC content",
		"budget": {"max_wall_clock": "30m", "max_tokens": 10000, "max_iterations": 20, "max_retries_per_task": 2},
		"git": {"isolation": "branch", "base_branch": "main", "work_branch": "run/demo", "commit_per_task": true, "merge_to_base": "manual"},
		"hitl": {"checkpoints": [{"id": "post-decomp", "trigger": "after_spec_decomposition", "required": true}, {"id": "pre-merge", "trigger": "before_merge", "required": true}]},
		"tasks": [{"id": "t1", "goal": "do thing"}]
	}`
	m, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.RunID != "demo" || m.Mode != "checkpoint" {
		t.Fatalf("parsed manifest mismatch: %+v", m)
	}
	if m.WallClockDuration().String() != "30m0s" {
		t.Fatalf("wall clock %q", m.WallClockDuration())
	}
	if !m.HasPreMergeRequired() {
		t.Error("expected pre-merge required")
	}
	if len(m.EffectiveTasks()) != 1 {
		t.Error("expected 1 effective task")
	}
}

func TestParseUnknownFieldRejected(t *testing.T) {
	data := `{"run_id": "x", "mode": "supervised", "goal": "g", "budget": {}, "no_such": true}`
	if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "no_such") {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
}

func TestValidateRejectsBadRunID(t *testing.T) {
	m := Manifest{RunID: "../evil", Mode: "supervised", Goal: "g"}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("expected run_id validation, got %v", err)
	}
}

func TestValidateBudgetDurations(t *testing.T) {
	m := Manifest{RunID: "ok", Mode: "supervised", Goal: "g", Budget: Budget{MaxWallClock: "not-a-duration"}}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "max_wall_clock") {
		t.Fatalf("expected duration error, got %v", err)
	}
}

func TestValidateCheckpointTriggers(t *testing.T) {
	cases := []struct {
		name    string
		cp      Checkpoint
		wantErr string
	}{
		{"unknown trigger", Checkpoint{ID: "c1", Trigger: "nope", Required: true}, "trigger"},
		{"budget threshold out of range", Checkpoint{ID: "c1", Trigger: "budget_threshold", Required: true, Threshold: 1.5}, "threshold"},
		{"before_editing without match", Checkpoint{ID: "c1", Trigger: "before_editing", Required: true}, "match"},
		{"duplicate id", Checkpoint{ID: "dup", Trigger: "before_task", Required: true}, "duplicated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Manifest{RunID: "ok", Mode: "supervised", Goal: "g", HITL: HITLConfig{Checkpoints: []Checkpoint{tc.cp}}}
			// duplicate id case needs two checkpoints
			if tc.name == "duplicate id" {
				m.HITL.Checkpoints = append(m.HITL.Checkpoints, Checkpoint{ID: "dup", Trigger: "after_task", Required: false})
			}
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestEffectiveTasksFallback(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "supervised", Goal: "do foo"}
	if tasks := m.EffectiveTasks(); len(tasks) != 1 || tasks[0].Goal != "do foo" {
		t.Fatalf("fallback tasks %v", tasks)
	}
}

func TestIsolationRequired(t *testing.T) {
	if (&Manifest{Mode: "supervised"}).IsolationRequired() {
		t.Error("supervised should not require isolation")
	}
	if (&Manifest{Mode: "dry_run"}).IsolationRequired() {
		t.Error("dry_run should not require isolation")
	}
	if !(&Manifest{Mode: "checkpoint"}).IsolationRequired() {
		t.Error("checkpoint requires isolation")
	}
	if !(&Manifest{Mode: "autonomous"}).IsolationRequired() {
		t.Error("autonomous requires isolation")
	}
}

func TestValidateAgainstSensitivity(t *testing.T) {
	m := Manifest{RunID: "r", Mode: "autonomous", Goal: "g"}
	cfg := config.Defaults()
	cfg.Project.Sensitivity = config.SensitivityGeneral
	if err := m.ValidateAgainstSensitivity(cfg); err != nil {
		t.Fatalf("general should allow autonomous: %v", err)
	}
	cfg.Project.Sensitivity = config.SensitivitySensitive
	if err := m.ValidateAgainstSensitivity(cfg); err == nil {
		t.Fatal("sensitive should block autonomous")
	}
	// regulated without pre-merge
	m2 := Manifest{RunID: "r", Mode: "checkpoint", Goal: "g"}
	cfg.Project.Sensitivity = config.SensitivityRegulated
	if err := m2.ValidateAgainstSensitivity(cfg); err == nil || !strings.Contains(err.Error(), "before_merge") {
		t.Fatalf("regulated checkpoint without pre-merge should fail, got %v", err)
	}
	m2.HITL = HITLConfig{Checkpoints: []Checkpoint{{ID: "pm", Trigger: "before_merge", Required: true}}}
	if err := m2.ValidateAgainstSensitivity(cfg); err != nil {
		t.Fatalf("regulated with pre-merge should pass: %v", err)
	}
}

func TestParseFileResolvesSpecRef(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("# spec content"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestJSON := `{"run_id": "r1", "mode": "supervised", "goal": "do it", "spec_ref": "SPEC.md", "budget": {"max_wall_clock": "1h"}}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	m, err := ParseFile(manifestPath)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if !strings.Contains(m.Spec, "spec content") {
		t.Fatalf("spec not resolved: %q", m.Spec)
	}
}
