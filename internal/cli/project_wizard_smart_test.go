package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/bootstrap"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/run"
)

func TestParseIndexList(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"1,2,3", []int{1, 2, 3}, false},
		{"1, 2 ,3", []int{1, 2, 3}, false},
		{"7", []int{7}, false},
		{"", nil, true},
		{"   ", nil, true},
		{"1,,3", []int{1, 3}, false},
		{"1,abc", nil, true},
	}
	for _, c := range cases {
		got, err := parseIndexList(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseIndexList(%q): expected an error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseIndexList(%q): unexpected error: %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parseIndexList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseIndexList(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestParseClarifyCommand(t *testing.T) {
	t.Run("index only (two-step form)", func(t *testing.T) {
		idx, question, err := parseClarifyCommand("5")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if idx != 5 || question != "" {
			t.Errorf("got idx=%d question=%q, want idx=5 question=\"\"", idx, question)
		}
	})

	t.Run("inline question (one-line form)", func(t *testing.T) {
		idx, question, err := parseClarifyCommand("5 ¿implica que un usuario puede estar en dos grupos?")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if idx != 5 {
			t.Errorf("idx = %d, want 5", idx)
		}
		if question != "¿implica que un usuario puede estar en dos grupos?" {
			t.Errorf("question = %q", question)
		}
	})

	t.Run("not a number", func(t *testing.T) {
		if _, _, err := parseClarifyCommand("abc"); err == nil {
			t.Fatal("expected an error for a non-numeric index")
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, _, err := parseClarifyCommand(""); err == nil {
			t.Fatal("expected an error for an empty command")
		}
	})
}

// TestWizardSmart_MissingDaemon_NoFilesWritten confirms this command fails
// the same way `forge run` does without a daemon (daemonHint's message),
// and — crucially — never writes anything to disk when it fails this
// early, same guarantee TestRunWizard_ExistingDir_ChooseNewName locks in
// for the classic wizard's own gate.
func TestWizardSmart_MissingDaemon_NoFilesWritten(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	p := NewScriptedPrompter(nil) // never reached: fails before any prompt
	var out bytes.Buffer
	err := runWizardSmart(context.Background(), p, &out, "nuevo-proyecto")
	if err == nil {
		t.Fatal("expected failure without a running daemon")
	}
	if !strings.Contains(err.Error(), "forge serve") {
		t.Errorf("expected the forge-serve hint, got %v", err)
	}
	if _, statErr := os.Stat("nuevo-proyecto"); !os.IsNotExist(statErr) {
		t.Error("wizard smart must not create anything before reaching the daemon")
	}
}

// TestWizardSmart_ExistingDir_ChooseNewName mirrors
// TestRunWizard_ExistingDir_ChooseNewName: the create-vs-existing-project
// gate is shared code (resolveWizardDir), but this locks in that the smart
// wizard actually calls it too, not just the classic one.
func TestWizardSmart_ExistingDir_ChooseNewName(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("taken", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("taken", "marker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewScriptedPrompter([]string{
		"2", // "elegir otro nombre"
		"",  // new name left blank -> cancels
	})
	var out bytes.Buffer
	err := runWizardSmart(context.Background(), p, &out, "taken")
	if err == nil || !strings.Contains(err.Error(), "cancelado") {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
	// The pre-existing directory must be untouched — no daemon call, no
	// overwrite, since the user picked "elegir otro nombre".
	entries, rdErr := os.ReadDir("taken")
	if rdErr != nil {
		t.Fatalf("ReadDir: %v", rdErr)
	}
	if len(entries) != 1 || entries[0].Name() != "marker.txt" {
		t.Errorf("existing directory was modified: %v", entries)
	}
}

// TestWriteSmartArtifacts_WritesValidFiles builds a bootstrap.Artifacts by
// hand (same shape bootstrap.finalize's real handler returns, per
// internal/bootstrap/finalize_test.go) and checks the three files it
// writes are actually valid/loadable — same rigor
// TestRunWizard_HappyPath applies to the classic wizard's output.
func TestWriteSmartArtifacts_WritesValidFiles(t *testing.T) {
	t.Chdir(t.TempDir())

	art := &bootstrap.Artifacts{
		SpecMD: "# SPEC\n\n## 1. Objetivo\n\nUna idea de prueba.\n\n## 2. Requisitos funcionales\n\n- **RF-1**: hacer algo\n",
		Config: config.Defaults(),
		Manifest: &run.Manifest{
			RunID:   "idea-derived-01", // must be overridden by the real dir-derived slug
			Mode:    "checkpoint",
			Goal:    "Una idea de prueba.",
			SpecRef: "SPEC.md",
			Budget: run.Budget{
				MaxWallClock:      "60m",
				MaxTokens:         150000,
				MaxIterations:     300,
				MaxRetriesPerTask: 2,
			},
			Git: run.GitConfig{Isolation: "worktree", CommitPerTask: true},
			HITL: run.HITLConfig{Checkpoints: []run.Checkpoint{
				{ID: "cp-before-merge", Trigger: run.TriggerBeforeMerge, Required: true},
			}},
			Tasks: []run.Task{{ID: "t1", Goal: "hacer algo", DoneCriteria: "cmd: go build ./..."}},
		},
	}

	var out bytes.Buffer
	if err := writeSmartArtifacts(&out, "Mi Proyecto Smart", art); err != nil {
		t.Fatalf("writeSmartArtifacts: %v", err)
	}

	dir := "Mi Proyecto Smart"
	specPath := filepath.Join(dir, "SPEC.md")
	cfgPath := filepath.Join(dir, ".forge", "config.json")
	manifestPath := filepath.Join(dir, "run.json")

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read SPEC.md: %v", err)
	}
	if !strings.Contains(string(spec), "RF-1") {
		t.Errorf("SPEC.md missing expected content: %s", spec)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config.json failed to Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config.json failed Validate: %v", err)
	}

	mani, err := run.ParseFile(manifestPath)
	if err != nil {
		t.Fatalf("generated run.json failed to ParseFile: %v", err)
	}
	if err := mani.Validate(); err != nil {
		t.Fatalf("generated run.json failed Validate: %v", err)
	}
	if mani.RunID != "mi-proyecto-smart-01" {
		t.Errorf("RunID = %q, want the directory-derived slug, not the idea-derived one Finalize proposed", mani.RunID)
	}

	if !strings.Contains(out.String(), dir) {
		t.Error("summary output should mention the created directory")
	}
}
