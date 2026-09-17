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

// TestRunWizard_HappyPath drives the full Q&A over a ScriptedPrompter (the
// same fake-input mechanism TestPluginWizard_GeneratesValidManifest /
// TestSkillWizard_* use) for a fresh project directory, and checks the
// three generated files carry the actual answers — not placeholders — and
// still pass forge's own Load/Validate, same rigor as
// TestRunInit_ScaffoldsValidLoadableFiles.
func TestRunWizard_HappyPath(t *testing.T) {
	t.Chdir(t.TempDir())

	p := NewScriptedPrompter([]string{
		"Una app de gestión de tareas para equipos chicos.", // description
		"Crear tareas",          // feature 1
		"Asignar responsables",  // feature 2
		"",                      // end features
		"",                      // end non-functional (empty)
		"Integración con Slack", // out of scope 1
		"",                      // end out of scope
		"Go + SQLite, CLI",      // architecture
		"",                      // sensitivity: default (general)
		"",                      // provider: default (ollama)
		"",                      // model: default
		"",                      // goal: default (uses description)
	})

	var out bytes.Buffer
	if err := runWizard(p, &out, "Test Project"); err != nil {
		t.Fatalf("runWizard: %v out=%s", err, out.String())
	}

	dir := "Test Project"
	specPath := filepath.Join(dir, "SPEC.md")
	cfgPath := filepath.Join(dir, ".forge", "config.json")
	manifestPath := filepath.Join(dir, "run.json")

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read SPEC.md: %v", err)
	}
	for _, want := range []string{
		"Una app de gestión de tareas para equipos chicos.",
		"**RF-1**: Crear tareas",
		"**RF-2**: Asignar responsables",
		"Integración con Slack",
		"Go + SQLite, CLI",
	} {
		if !strings.Contains(string(spec), want) {
			t.Errorf("SPEC.md missing %q", want)
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config.json failed to Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config.json failed Validate: %v", err)
	}
	if cfg.Project.Sensitivity != config.SensitivityGeneral {
		t.Errorf("sensitivity = %q, want general", cfg.Project.Sensitivity)
	}
	if _, ok := cfg.Providers["ollama"]; !ok {
		t.Errorf("expected default ollama provider, got %+v", cfg.Providers)
	}

	mani, err := run.ParseFile(manifestPath)
	if err != nil {
		t.Fatalf("generated run.json failed to ParseFile: %v", err)
	}
	if err := mani.Validate(); err != nil {
		t.Fatalf("generated run.json failed Validate: %v", err)
	}
	if mani.Goal != "Una app de gestión de tareas para equipos chicos." {
		t.Errorf("goal = %q, want the description default", mani.Goal)
	}

	if !strings.Contains(out.String(), dir) {
		t.Error("summary output should mention the created directory")
	}
}

// TestRunWizard_ExistingDir_ChooseNewName covers the explicit safety
// requirement: pointing the wizard at an existing, non-empty project must
// never silently overwrite it — here the user picks "elegir otro nombre".
func TestRunWizard_ExistingDir_ChooseNewName(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("taken", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("taken", "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewScriptedPrompter([]string{
		"2",             // choose "elegir otro nombre"
		"taken-renamed", // new name
		"desc",          // description
		"",              // features (empty)
		"",              // non-functional (empty)
		"",              // out of scope (empty)
		"arch",          // architecture
		"",              // sensitivity default
		"",              // provider default
		"",              // model default
		"",              // goal default
	})

	var out bytes.Buffer
	if err := runWizard(p, &out, "taken"); err != nil {
		t.Fatalf("runWizard: %v out=%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join("taken", "run.json")); err == nil {
		t.Error("original 'taken' directory should not have been touched")
	}
	if _, err := os.Stat(filepath.Join("taken-renamed", "run.json")); err != nil {
		t.Errorf("expected files scaffolded under the renamed directory: %v", err)
	}
}

// TestRunWizard_ExistingDir_ConfirmOverwrite covers the other half of the
// gate: the user may proceed on an existing directory, but only after an
// explicit yes/no confirmation distinct from the initial menu choice.
func TestRunWizard_ExistingDir_ConfirmOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("taken", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("taken", "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewScriptedPrompter([]string{
		"1", // choose "continuar completando este proyecto existente"
		"y", // confirm overwrite
		"desc",
		"",
		"",
		"",
		"arch",
		"",
		"",
		"",
		"",
	})

	var out bytes.Buffer
	if err := runWizard(p, &out, "taken"); err != nil {
		t.Fatalf("runWizard: %v out=%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join("taken", "run.json")); err != nil {
		t.Errorf("expected files scaffolded into the confirmed existing directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join("taken", "existing.txt")); err != nil {
		t.Error("wizard should not remove files it didn't create")
	}
}

// TestRunWizard_RemoteProvider checks the branch that asks extra questions
// (base_url) and writes api_key as an explicit placeholder rather than
// leaving it blank (blank silently falls back to an env var per config's
// own doc comment — a human filling this template in needs to see it needs
// filling).
func TestRunWizard_RemoteProvider(t *testing.T) {
	t.Chdir(t.TempDir())

	p := NewScriptedPrompter([]string{
		"desc",
		"",
		"",
		"",
		"arch",
		"",  // sensitivity default
		"2", // provider: remote openai-compatible
		"https://api.example.com/v1",
		"gpt-4o-mini",
		"", // goal default
	})

	var out bytes.Buffer
	if err := runWizard(p, &out, "Remote Project"); err != nil {
		t.Fatalf("runWizard: %v out=%s", err, out.String())
	}

	cfgPath := filepath.Join("Remote Project", ".forge", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config.json failed to Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config.json failed Validate: %v", err)
	}
	remote, ok := cfg.Providers["remote"]
	if !ok {
		t.Fatalf("expected a 'remote' provider, got %+v", cfg.Providers)
	}
	if remote.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url = %q, want the answered URL", remote.BaseURL)
	}
	if remote.APIKey == "" {
		t.Error("api_key should be an explicit placeholder for a remote provider, not blank")
	}

	found := false
	for _, h := range cfg.Network.AllowedHosts {
		if h == "api.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("network.allowed_hosts = %v, want it to include the remote provider's host — otherwise every call fails the egress allowlist", cfg.Network.AllowedHosts)
	}
}

// TestRunWizard_AnthropicProviderAllowsHost is the same allowlist regression
// for the Anthropic branch, whose base_url isn't answered by the user (it's
// hardcoded to api.anthropic.com) so this is the only place it gets covered.
func TestRunWizard_AnthropicProviderAllowsHost(t *testing.T) {
	t.Chdir(t.TempDir())

	p := NewScriptedPrompter([]string{
		"desc",
		"",
		"",
		"",
		"arch",
		"",                           // sensitivity default
		"3",                          // provider: anthropic
		"claude-3-5-sonnet-20241022", // model
		"",                           // goal default
	})

	var out bytes.Buffer
	if err := runWizard(p, &out, "Anthropic Project"); err != nil {
		t.Fatalf("runWizard: %v out=%s", err, out.String())
	}

	cfgPath := filepath.Join("Anthropic Project", ".forge", "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config.json failed to Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("generated config.json failed Validate: %v", err)
	}
	anthropic, ok := cfg.Providers["anthropic"]
	if !ok {
		t.Fatalf("expected an 'anthropic' provider, got %+v", cfg.Providers)
	}
	if anthropic.Models[0] != "claude-3-5-sonnet-20241022" {
		t.Errorf("model = %q, want the answered model", anthropic.Models[0])
	}

	found := false
	for _, h := range cfg.Network.AllowedHosts {
		if h == "api.anthropic.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("network.allowed_hosts = %v, want it to include api.anthropic.com", cfg.Network.AllowedHosts)
	}
}

// TestRunWizard_RejectsEmptyName mirrors init's own guard: the wizard
// shouldn't attempt to scaffold anything for a blank project name.
func TestRunWizard_RejectsEmptyName(t *testing.T) {
	t.Chdir(t.TempDir())
	p := NewScriptedPrompter(nil)
	var out bytes.Buffer
	if err := runWizard(p, &out, "   "); err == nil {
		t.Fatal("expected an error for a blank project name")
	}
}
