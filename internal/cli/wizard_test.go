package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/plugin"
	"github.com/eduardosanmartin/forge/internal/skill"
)

func TestPluginWizard_GeneratesValidManifest(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "forge-plugins")
	// Scripted inputs: name, version, description, 5 perms bools (all false -> defaults fs.read), entrypoint, source
	values := []string{
		"my_plugin",       // name
		"0.1.0",           // version
		"Test plugin",     // description
		"n", "n", "n", "n", "n", // 5 perms (all false)
		"",                // entrypoint -> default
		"local",           // source
	}
	p := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runPluginWizard(p, &out, pluginsRoot, false); err != nil {
		t.Fatalf("wizard failed: %v out=%s", err, out.String())
	}
	manifestPath := filepath.Join(pluginsRoot, "my_plugin", "manifest.toml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest not created: %v", err)
	}
	if _, err := plugin.ParseManifest(data); err != nil {
		t.Fatalf("generated manifest failed ParseManifest: %v\ncontent:\n%s", err, string(data))
	}
	// Check required scaffold files
	for _, f := range []string{"Cargo.toml", "src/lib.rs", "README.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(pluginsRoot, "my_plugin", f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	// Check Cargo.toml content
	cargo, _ := os.ReadFile(filepath.Join(pluginsRoot, "my_plugin", "Cargo.toml"))
	if !strings.Contains(string(cargo), `crate-type=["cdylib"]`) && !strings.Contains(string(cargo), `crate-type = ["cdylib"]`) {
		t.Fatalf("Cargo.toml missing cdylib crate-type: %s", string(cargo))
	}
	lib, _ := os.ReadFile(filepath.Join(pluginsRoot, "my_plugin", "src/lib.rs"))
	s := string(lib)
	for _, needle := range []string{"forge_abi_version", "forge_alloc", "forge_tool_list", "forge_tool_invoke", "forge_host", "verified in WU6"} {
		if !strings.Contains(s, needle) {
			t.Fatalf("lib.rs missing %q", needle)
		}
	}
}

func TestPluginWizard_InvalidNameReprompt(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "forge-plugins")
	// First name invalid (uppercase / dot), second valid
	values := []string{
		"Bad-Name!",        // invalid
		"good_name",        // valid retried
		"0.1.0",            // version
		"desc",             // description
		"n", "n", "n", "n", "n",
		"",                 // entrypoint
		"local",
	}
	p := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runPluginWizard(p, &out, pluginsRoot, false); err != nil {
		t.Fatalf("wizard failed after reprompt: %v out=%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(pluginsRoot, "good_name", "manifest.toml")); err != nil {
		t.Fatalf("expected good_name plugin created: %v out=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "invalid name") {
		t.Fatalf("expected reprompt message, out=%s", out.String())
	}
}

func TestPluginWizard_ForceOverwrite(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "forge-plugins")
	values := []string{"dup_plugin", "0.1.0", "desc", "n", "n", "n", "n", "n", "", "local"}
	p1 := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runPluginWizard(p1, &out, pluginsRoot, false); err != nil {
		t.Fatalf("first wizard: %v", err)
	}
	// Second without force should fail
	p2 := NewScriptedPrompter(values)
	var out2 bytes.Buffer
	if err := runPluginWizard(p2, &out2, pluginsRoot, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists error, got %v out2=%s", err, out2.String())
	}
	// With force should succeed
	p3 := NewScriptedPrompter(values)
	var out3 bytes.Buffer
	if err := runPluginWizard(p3, &out3, pluginsRoot, true); err != nil {
		t.Fatalf("force overwrite failed: %v", err)
	}
}

func TestSkillWizard_GeneratesValidSkill(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, ".forge", "skills")
	values := []string{
		"my-skill",          // name
		"A skill for tests", // description
		"review",            // category
		"kw1, kw2",          // keywords
		"",                  // scripts (none)
		"local",             // source
	}
	p := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runSkillWizard(p, &out, skillsRoot, false); err != nil {
		t.Fatalf("skill wizard failed: %v out=%s", err, out.String())
	}
	skillPath := filepath.Join(skillsRoot, "my-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("SKILL.md not created: %v", err)
	}
	// Validate via skill manager scan
	tmpRoot := filepath.Dir(filepath.Join(skillsRoot, "my-skill"))
	_ = tmpRoot
	mgr := skill.NewManager(skill.Options{ApproveExternal: true})
	defer mgr.Close()
	results, err := mgr.Scan(skillsRoot)
	if err != nil {
		t.Fatalf("skill scan failed: %v", err)
	}
	if len(results) == 0 || !results[0].Loaded {
		t.Fatalf("skill not loaded: %+v", results)
	}
	_ = data
}

func TestSkillWizard_WithScriptsCreatesFiles(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, ".forge", "skills")
	values := []string{
		"with-scripts",
		"desc",
		"",
		"",
		"scripts/a.sh, scripts/b.sh",
		"local",
	}
	p := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runSkillWizard(p, &out, skillsRoot, false); err != nil {
		t.Fatalf("wizard: %v", err)
	}
	for _, s := range []string{"scripts/a.sh", "scripts/b.sh"} {
		if _, err := os.Stat(filepath.Join(skillsRoot, "with-scripts", s)); err != nil {
			t.Fatalf("script %s not created: %v", s, err)
		}
	}
}

func TestSkillWizard_InvalidNameReprompt(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, ".forge", "skills")
	values := []string{
		"Bad Name!", // invalid
		"good-skill", // valid
		"desc",
		"",
		"",
		"",
		"local",
	}
	p := NewScriptedPrompter(values)
	var out bytes.Buffer
	if err := runSkillWizard(p, &out, skillsRoot, false); err != nil {
		t.Fatalf("wizard failed: %v out=%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(skillsRoot, "good-skill", "SKILL.md")); err != nil {
		t.Fatalf("good-skill not created: %v out=%s", err, out.String())
	}
}
