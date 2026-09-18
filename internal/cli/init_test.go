package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/run"
)

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"wordstat-multiagente", "wordstat-multiagente"},
		{"My Cool Project", "my-cool-project"},
		{"under_scores_here", "under-scores-here"},
		{"  spaces  around  ", "spaces-around"},
		{"Ñoño!!!", "oo"}, // punctuation and non a-z stripped, "Ñ" is not [a-z]
		{"!!!", "project"},
		{"", "project"},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRunInit_ScaffoldsValidLoadableFiles is the real regression lock: it
// doesn't just check the 3 files exist, it runs them through forge's own
// config.Load/Validate and run.ParseFile/Validate — the exact same
// validation every real `forge run`/`forge serve` invocation performs. A
// template that merely "looks like JSON" isn't good enough; this is the
// same rigor applied by hand to the wordstat-multiagente/chores-cli
// examples earlier this session, now permanent instead of a throwaway test.
func TestRunInit_ScaffoldsValidLoadableFiles(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := runInit(&out, "My Test Project", false); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	dir := "My Test Project"
	specPath := filepath.Join(dir, "SPEC.md")
	cfgPath := filepath.Join(dir, ".forge", "config.json")
	manifestPath := filepath.Join(dir, "run.json")

	for _, p := range []string{specPath, cfgPath, manifestPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", p, err)
		}
	}

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read SPEC.md: %v", err)
	}
	if !strings.Contains(string(spec), "My Test Project") {
		t.Error("SPEC.md should name the project")
	}
	if !strings.Contains(string(spec), "RF-1") {
		t.Error("SPEC.md should carry the RF-N template structure")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config.json failed to Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config.json failed Validate: %v", err)
	}
	if cfg.Project.SpecPath != "SPEC.md" {
		t.Errorf("project.spec_path = %q, want SPEC.md", cfg.Project.SpecPath)
	}

	// Regression lock: every section shows forge's REAL default values in
	// the raw written JSON, not Go zero-values that happen to be papered
	// over by config.Load's own merge-in-defaults step. A template a human
	// is meant to read and edit must show real numbers, not misleading
	// blanks/zeros for sections the author didn't happen to set explicitly.
	rawCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	for _, want := range []string{
		`"level": "info"`,
		`"layout": "hybrid"`,
		`"palette": "ember"`,
		`"sidebar": true`,
		`"plugin_wasm_max_bytes": 2097152`,
		`"skill_file_max_bytes": 1048576`,
		// Regression lock for scaffoldAgentMaxIterations: the raw library
		// default (10) is a per-turn tool-call cap that a real single-task
		// manifest run exhausted mid-task (a handful of fs_read/fs_write
		// calls), forcing an avoidable retry — see init.go's doc comment.
		`"max_iterations": 30`,
	} {
		if !strings.Contains(string(rawCfg), want) {
			t.Errorf("config.json missing %s — a zero-value field leaked through instead of a real default", want)
		}
	}

	mani, err := run.ParseFile(manifestPath)
	if err != nil {
		t.Fatalf("generated run.json failed to ParseFile: %v", err)
	}
	if err := mani.Validate(); err != nil {
		t.Fatalf("generated run.json failed Validate: %v", err)
	}
	if mani.RunID != "my-test-project-01" {
		t.Errorf("run_id = %q, want my-test-project-01 (slugified)", mani.RunID)
	}
	if len(mani.Tasks) != 0 {
		t.Errorf("expected empty tasks (ready for --decompose), got %d", len(mani.Tasks))
	}
	if mani.Spec == "" {
		t.Error("spec_ref should have resolved SPEC.md's content into Spec")
	}

	if !strings.Contains(out.String(), dir) {
		t.Error("summary output should mention the created directory")
	}
}

func TestRunInit_RefusesNonEmptyDirectoryWithoutForce(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("taken", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("taken", "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runInit(&out, "taken", false)
	if err == nil {
		t.Fatal("expected an error scaffolding into a non-empty directory without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force as the way out, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join("taken", "run.json")); statErr == nil {
		t.Error("run.json should not have been written when init refused")
	}
}

func TestRunInit_ForceScaffoldsIntoExistingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("taken", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("taken", "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInit(&out, "taken", true); err != nil {
		t.Fatalf("runInit with --force: %v", err)
	}
	if _, err := os.Stat(filepath.Join("taken", "run.json")); err != nil {
		t.Errorf("run.json should exist after --force scaffold: %v", err)
	}
	if _, err := os.Stat(filepath.Join("taken", "existing.txt")); err != nil {
		t.Error("--force should not remove files it didn't create")
	}
}

func TestRunInit_RejectsPathSeparatorsInName(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	for _, bad := range []string{"a/b", `a\b`, "../escape", "."} {
		err := runInit(&out, bad, false)
		if bad == "." {
			continue // "." is a valid (if unusual) relative dir name, not tested here
		}
		if err == nil {
			t.Errorf("expected runInit(%q) to fail", bad)
		}
	}
}
