package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsSensitivityGeneral(t *testing.T) {
	cfg := Defaults()
	if cfg.Project.Sensitivity != SensitivityGeneral {
		t.Fatalf("default sensitivity %q want %q", cfg.Project.Sensitivity, SensitivityGeneral)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
}

func TestSensitivityAliasNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"general", SensitivityGeneral},
		{"low", SensitivityGeneral},
		{"GENERAL", SensitivityGeneral},
		{"regulado", SensitivityRegulated},
		{"regulated", SensitivityRegulated},
		{"medium", SensitivityRegulated},
		{"datos-sensibles", SensitivitySensitive},
		{"sensitive", SensitivitySensitive},
		{"high", SensitivitySensitive},
		{"datos_sensibles", SensitivitySensitive},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			writeConfigFile(t, p, `{"project":{"sensitivity":`+`"`+tc.in+`"`+`}}`)
			got, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.Project.Sensitivity != tc.want {
				t.Fatalf("sensitivity %q normalized to %q want %q", tc.in, got.Project.Sensitivity, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestSensitivityInvalidRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfigFile(t, p, `{"project":{"sensitivity":"unknown"}}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "project.sensitivity") {
		t.Fatalf("Validate() = %v, want sensitivity violation", err)
	}
}

func TestSensitivityMissingDefaultsGeneral(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeConfigFile(t, p, `{}`)
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Project.Sensitivity != SensitivityGeneral {
		t.Fatalf("missing sensitivity got %q want %q", got.Project.Sensitivity, SensitivityGeneral)
	}
}

func TestAutonomyCeilingEnforcement(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		sens       string
		hasPreMerge bool
		wantErr    bool
		errSubstr  string
	}{
		{"general allows autonomous", "autonomous", "general", false, false, ""},
		{"general allows checkpoint", "checkpoint", "general", false, false, ""},
		{"regulated blocks autonomous", "autonomous", "regulado", true, true, "exceeds"},
		{"regulated allows checkpoint with pre-merge", "checkpoint", "regulado", true, false, ""},
		{"regulated rejects checkpoint without pre-merge", "checkpoint", "regulado", false, true, "before_merge"},
		{"sensitive blocks checkpoint", "checkpoint", "datos-sensibles", true, true, "exceeds"},
		{"sensitive blocks autonomous", "autonomous", "datos-sensibles", true, true, "exceeds"},
		{"sensitive allows supervised", "supervised", "datos-sensibles", false, false, ""},
		{"sensitive allows dry_run", "dry_run", "datos-sensibles", false, false, ""},
		{"alias low allows autonomous", "autonomous", "low", false, false, ""},
		{"alias high blocks autonomous", "autonomous", "high", false, true, "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAutonomyAgainstSensitivity(tc.mode, tc.sens, tc.hasPreMerge)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

func TestAutonomyRankUnknown(t *testing.T) {
	if AutonomyRank("unknown") != -1 {
		t.Error("unknown mode should return -1")
	}
	if SensitivityRank("unknown") != -1 {
		t.Error("unknown sensitivity should return -1")
	}
}

func TestSaveLoadRoundTripsSensitivity(t *testing.T) {
	cfg := Defaults()
	cfg.Project.Sensitivity = SensitivitySensitive
	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Project.Sensitivity != SensitivitySensitive {
		t.Fatalf("round-trip sensitivity %q want %q", got.Project.Sensitivity, SensitivitySensitive)
	}
}

func TestExampleProjectSensitivityPresent(t *testing.T) {
	examplePath := filepath.Join("..", "..", "configs", "forge.json.example")
	cfg, err := Load(examplePath)
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if cfg.Project.Sensitivity != SensitivityGeneral {
		t.Fatalf("example sensitivity %q want %q", cfg.Project.Sensitivity, SensitivityGeneral)
	}
}
