package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
)

func writeSizedPlugin(t *testing.T, dir, name, source string, perms []string, wasmSize int) string {
	t.Helper()
	pluginDir := filepath.Join(dir, name+"_src")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wasmBytes := bytes.Repeat([]byte("a"), wasmSize)
	sum := sha256.Sum256(wasmBytes)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	permStr := ""
	if len(perms) > 0 {
		quoted := make([]string, len(perms))
		for i, p := range perms {
			quoted[i] = `"` + p + `"`
		}
		permStr = strings.Join(quoted, ", ")
	}
	manifest := ""
	if source == "external" {
		manifest = "name = \"" + name + "\"\nversion = \"0.1.0\"\ndescription = \"test\"\nsource = \"external\"\nentrypoint = \"plugin.wasm\"\npermissions = [" + permStr + "]\nchecksum = \"" + checksum + "\"\n\n[[tools]]\nname = \"" + name + "_hello\"\ndescription = \"hello\"\npermission = \"" + perms[0] + "\"\n"
	} else {
		manifest = "name = \"" + name + "\"\nversion = \"0.1.0\"\ndescription = \"test\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\npermissions = [" + permStr + "]\n\n[[tools]]\nname = \"" + name + "_hello\"\ndescription = \"hello\"\npermission = \"" + perms[0] + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}
	return pluginDir
}

func writeSizedSkill(t *testing.T, dir, name string, files map[string]int) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Build SKILL.md frontmatter; body size can be controlled via files["SKILL.md"] if given.
	skillMDSize, hasMDSize := files["SKILL.md"]
	baseContent := "---\nname: " + name + "\ndescription: \"test skill for limits\"\nsource: local\n---\nBody\n"
	if hasMDSize {
		// Pad body to reach desired size
		bodyPadding := skillMDSize - len(baseContent)
		if bodyPadding > 0 {
			baseContent += strings.Repeat("x", bodyPadding)
		}
		// Remove entry so we don't double write
		delete(files, "SKILL.md")
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(baseContent), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	for rel, size := range files {
		full := filepath.Join(skillDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		data := bytes.Repeat([]byte("s"), size)
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return skillDir
}

func TestLimits_PluginWasmTooLargeRejected(t *testing.T) {
	srcRoot := t.TempDir()
	pluginsRoot := t.TempDir()
	overSize := int(config.DefaultPluginWasmMaxBytes + 1)
	src := writeSizedPlugin(t, srcRoot, "bigplug", "local", []string{"fs.read"}, overSize)
	var out bytes.Buffer
	err := runPluginInstall(src, pluginsRoot, false, false, NewScriptedPrompter(nil), &out)
	if err == nil {
		t.Fatalf("expected plugin too large error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"plugin wasm too large", "limits.plugin_wasm_max_bytes", "2097152"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	if !strings.Contains(msg, "2097153") && !strings.Contains(strings.ReplaceAll(msg, " ", ""), "2097153") {
		// Allow any message containing actual size; check that limit part is present
		if !strings.Contains(msg, "bytes > limit") {
			t.Errorf("error %q should contain actual size and limit", msg)
		}
	}
}

func TestLimits_PluginWasmAtLimitAllowed(t *testing.T) {
	srcRoot := t.TempDir()
	pluginsRoot := t.TempDir()
	atSize := int(config.DefaultPluginWasmMaxBytes)
	src := writeSizedPlugin(t, srcRoot, "atplug", "local", []string{"fs.read"}, atSize)
	var out bytes.Buffer
	if err := runPluginInstall(src, pluginsRoot, false, false, NewScriptedPrompter(nil), &out); err != nil {
		t.Fatalf("install at limit should succeed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginsRoot, "atplug", "plugin.wasm")); err != nil {
		t.Fatalf("installed wasm missing: %v", err)
	}
}

func TestLimits_PluginWasmUnderLimitAllowed(t *testing.T) {
	srcRoot := t.TempDir()
	pluginsRoot := t.TempDir()
	src := writeSizedPlugin(t, srcRoot, "smallplug", "local", []string{"fs.read"}, 1024)
	var out bytes.Buffer
	if err := runPluginInstall(src, pluginsRoot, false, false, NewScriptedPrompter(nil), &out); err != nil {
		t.Fatalf("small plugin should install: %v", err)
	}
}

func TestLimits_SkillFileTooLargeRejected(t *testing.T) {
	srcRoot := t.TempDir()
	skillsRoot := t.TempDir()
	overSize := int(config.DefaultSkillFileMaxBytes + 1)
	src := writeSizedSkill(t, srcRoot, "bigskill", map[string]int{"SKILL.md": overSize})
	var out bytes.Buffer
	err := runSkillInstall(src, skillsRoot, false, false, NewScriptedPrompter(nil), &out)
	if err == nil {
		t.Fatalf("expected skill file too large, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"skill file too large", "limits.skill_file_max_bytes", "1048576"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestLimits_SkillPerFileSemantics(t *testing.T) {
	srcRoot := t.TempDir()
	skillsRoot := t.TempDir()
	under := int(config.DefaultSkillFileMaxBytes - 10)
	// Two files each under limit → should pass
	src := writeSizedSkill(t, srcRoot, "twoskill", map[string]int{
		"SKILL.md":            under,
		"scripts/a.sh":        under,
		"scripts/b.sh":        500,
	})
	var out bytes.Buffer
	if err := runSkillInstall(src, skillsRoot, false, false, NewScriptedPrompter(nil), &out); err != nil {
		t.Fatalf("two under-limit files should pass, got %v", err)
	}
	// One file over limit → should fail even if others are small
	srcRoot2 := t.TempDir()
	skillsRoot2 := t.TempDir()
	over := int(config.DefaultSkillFileMaxBytes + 1)
	src2 := writeSizedSkill(t, srcRoot2, "oneskill", map[string]int{
		"SKILL.md":     100,
		"scripts/a.sh": over,
	})
	var out2 bytes.Buffer
	err := runSkillInstall(src2, skillsRoot2, false, false, NewScriptedPrompter(nil), &out2)
	if err == nil || !strings.Contains(err.Error(), "skill file too large") {
		t.Fatalf("expected per-file reject, got %v", err)
	}
}

func TestLimits_ConfigOverrideSmallPluginLimit(t *testing.T) {
	// Prove configurability: a small limit rejects a file that the default would allow.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{"limits": {"plugin_wasm_max_bytes": 100}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Limits.PluginWasmMaxBytes != 100 {
		t.Fatalf("override not applied: got %d want 100", cfg.Limits.PluginWasmMaxBytes)
	}
	// Use the loaded limits directly to prove install respects override
	srcRoot := t.TempDir()
	pluginsRoot := t.TempDir()
	src := writeSizedPlugin(t, srcRoot, "cfgplug", "local", []string{"fs.read"}, 200)
	var out bytes.Buffer
	err = runPluginInstallWithLimits(src, pluginsRoot, false, false, NewScriptedPrompter(nil), &out, cfg.Limits)
	if err == nil || !strings.Contains(err.Error(), "plugin wasm too large") {
		t.Fatalf("expected reject with small limit 100, got %v", err)
	}
	if !strings.Contains(err.Error(), "limit 100") {
		t.Errorf("error %q should mention configured limit 100", err.Error())
	}
	// Smaller file under override should pass
	src2 := writeSizedPlugin(t, srcRoot, "cfgplug2", "local", []string{"fs.read"}, 50)
	var out2 bytes.Buffer
	if err := runPluginInstallWithLimits(src2, pluginsRoot, false, false, NewScriptedPrompter(nil), &out2, cfg.Limits); err != nil {
		t.Fatalf("file under small limit should pass, got %v", err)
	}
}

func TestLimits_MissingLimitsSectionDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Limits.PluginWasmMaxBytes != config.DefaultPluginWasmMaxBytes {
		t.Errorf("missing limits should default plugin wasm: got %d want %d", cfg.Limits.PluginWasmMaxBytes, config.DefaultPluginWasmMaxBytes)
	}
	if cfg.Limits.SkillFileMaxBytes != config.DefaultSkillFileMaxBytes {
		t.Errorf("missing limits should default skill file: got %d want %d", cfg.Limits.SkillFileMaxBytes, config.DefaultSkillFileMaxBytes)
	}
}

func TestLimits_ZeroNegativeFallsBackToDefault(t *testing.T) {
	cases := []string{
		`{"limits": {"plugin_wasm_max_bytes": 0, "skill_file_max_bytes": 0}}`,
		`{"limits": {"plugin_wasm_max_bytes": -1, "skill_file_max_bytes": -100}}`,
	}
	for i, content := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("case %d write: %v", i, err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("case %d Load: %v", i, err)
		}
		if cfg.Limits.PluginWasmMaxBytes != config.DefaultPluginWasmMaxBytes {
			t.Errorf("case %d: zero/negative plugin limit should fallback to default, got %d", i, cfg.Limits.PluginWasmMaxBytes)
		}
		if cfg.Limits.SkillFileMaxBytes != config.DefaultSkillFileMaxBytes {
			t.Errorf("case %d: zero/negative skill limit should fallback, got %d", i, cfg.Limits.SkillFileMaxBytes)
		}
		// Also prove install still uses defaults (2MiB limit, not 0)
		srcRoot := t.TempDir()
		pluginsRoot := t.TempDir()
		over := int(config.DefaultPluginWasmMaxBytes + 1)
		src := writeSizedPlugin(t, srcRoot, "fallback", "local", []string{"fs.read"}, over)
		var out bytes.Buffer
		err = runPluginInstallWithLimits(src, pluginsRoot, false, false, NewScriptedPrompter(nil), &out, cfg.Limits)
		if err == nil || !strings.Contains(err.Error(), "plugin wasm too large") {
			t.Errorf("case %d: fallback limits should still reject oversize, got %v", i, err)
		}
	}
}

func TestLimits_ConfigRoundTripPreservesLimits(t *testing.T) {
	cfg := config.Defaults()
	cfg.Limits.PluginWasmMaxBytes = 12345
	cfg.Limits.SkillFileMaxBytes = 6789
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Limits.PluginWasmMaxBytes != 12345 || got.Limits.SkillFileMaxBytes != 6789 {
		t.Errorf("round trip limits mismatch: got %+v", got.Limits)
	}
}

func TestLimits_SkillDiskLoadRespectsConfig(t *testing.T) {
	// End-to-end disk load path: write project config with small skill limit, chdir, then install via runSkillInstall (which loads disk).
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Ensure global config absent
	// Write project config in work/.forge/config.json
	if err := os.MkdirAll(filepath.Join(work, ".forge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	projCfg := `{"limits": {"skill_file_max_bytes": 100}}`
	if err := os.WriteFile(filepath.Join(work, ".forge", "config.json"), []byte(projCfg), 0o600); err != nil {
		t.Fatalf("write proj: %v", err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	srcRoot := t.TempDir()
	skillsRoot := t.TempDir()
	src := writeSizedSkill(t, srcRoot, "cfgsmall", map[string]int{"SKILL.md": 200})
	var out bytes.Buffer
	err := runSkillInstall(src, skillsRoot, false, false, NewScriptedPrompter(nil), &out)
	if err == nil || !strings.Contains(err.Error(), "skill file too large") {
		t.Fatalf("expected skill reject via disk-loaded small limit, got %v", err)
	}
	if !strings.Contains(err.Error(), "limit 100") {
		t.Errorf("error %q should mention limit 100 from project config", err.Error())
	}
}
