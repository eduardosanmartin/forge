package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExtSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := "---\nname: " + name + "\ndescription: \"external skill\"\nsource: external\nchecksum: \"PLACEHOLDER\"\n---\nBody\n"
	tmp := strings.Replace(base, "PLACEHOLDER", "sha256:"+strings.Repeat("a", 64), 1)
	clean := stripChecksumLine([]byte(tmp))
	sum := sha256.Sum256(clean)
	hexSum := hex.EncodeToString(sum[:])
	correct := "sha256:" + hexSum
	final := strings.Replace(base, "PLACEHOLDER", correct, 1)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(final), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestManager_Enable_ApprovalRecord_Skill(t *testing.T) {
	root := t.TempDir()
	dir := writeExtSkill(t, root, "ext-skill")

	// Case 1: ApproveExternal=false, no flag -> Scan should fail (not loaded)
	mgr1 := NewManager(Options{ApproveExternal: false})
	results, err := mgr1.Scan(root)
	if err == nil {
		t.Fatalf("expected scan failure without approval, got %+v", results)
	}
	if len(mgr1.Loaded()) != 0 {
		t.Fatalf("should not be loaded, got %v", mgr1.Loaded())
	}
	_ = mgr1.Close()

	// Case 2: With approved.flag containing correct hash -> Scan succeeds, Enable succeeds
	data, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	cleaned := StripChecksumLine(data)
	sum := sha256.Sum256(cleaned)
	flagHash := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte(flagHash), 0o644)
	mgr2 := NewManager(Options{ApproveExternal: false})
	results, err = mgr2.Scan(root)
	if err != nil {
		t.Fatalf("scan with flag should succeed: %v results %+v", err, results)
	}
	if len(mgr2.Loaded()) != 1 {
		t.Fatalf("expected loaded")
	}
	// Enable should succeed with flag
	if err := mgr2.Enable("ext-skill"); err != nil {
		t.Fatalf("Enable with flag: %v", err)
	}
	_ = mgr2.Close()
	// Case 2b: record present but artifact swapped -> denied
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: ext-skill\ndescription: \"external skill\"\nsource: external\nchecksum: \"sha256:"+strings.Repeat("0", 64)+"\"\n---\nTampered body\n"), 0o644)
	mgr2b := NewManager(Options{ApproveExternal: false})
	results, err = mgr2b.Scan(root)
	if err == nil {
		t.Fatalf("expected scan failure with mismatched hash, got %+v", results)
	}
	_ = mgr2b.Close()
	// Restore correct SKILL.md and correct flag, then test Enable with mismatched flag
	dir = writeExtSkill(t, root, "ext-skill") // rewrite correct file
	data, _ = os.ReadFile(filepath.Join(dir, "SKILL.md"))
	cleaned = StripChecksumLine(data)
	sum = sha256.Sum256(cleaned)
	flagHash = "sha256:" + hex.EncodeToString(sum[:]) + "\n"
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte(flagHash), 0o644)
	mgr2c := NewManager(Options{ApproveExternal: false})
	if _, err := mgr2c.Scan(root); err != nil {
		t.Fatalf("scan with correct flag should succeed: %v", err)
	}
	// Corrupt flag to wrong hash
	_ = os.WriteFile(filepath.Join(dir, "approved.flag"), []byte("sha256:"+strings.Repeat("0", 64)+"\n"), 0o644)
	if err := mgr2c.Enable("ext-skill"); err == nil || !strings.Contains(err.Error(), "approval record missing or does not match") {
		t.Fatalf("expected approval mismatch on Enable, got %v", err)
	}
	_ = mgr2c.Close()
	_ = os.Remove(filepath.Join(dir, "approved.flag"))

	// Case 3: ApproveExternal=true, no flag -> Scan and Enable succeed
	mgr3 := NewManager(Options{ApproveExternal: true})
	results, err = mgr3.Scan(root)
	if err != nil {
		t.Fatalf("scan with ApproveExternal true: %v", err)
	}
	if len(mgr3.Loaded()) != 1 {
		t.Fatalf("expected loaded")
	}
	// External is loaded but not enabled; Enable should succeed without flag
	if err := mgr3.Enable("ext-skill"); err != nil {
		t.Fatalf("Enable with ApproveExternal true: %v", err)
	}
	_ = mgr3.Close()
}

func TestSkill_InfoAndReload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "my-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: \"desc hello\"\ncategory: review\nsource: local\n---\nBody\n"), 0o644)

	mgr := NewManager(Options{})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	infos := mgr.Info()
	if len(infos) != 1 || infos[0].Name != "my-skill" || infos[0].Description != "desc hello" || infos[0].Category != "review" || !infos[0].Enabled {
		t.Fatalf("Info mismatch: %+v", infos)
	}
	// Disable then reload
	if err := mgr.Disable("my-skill"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if mgr.Info()[0].Enabled {
		t.Fatalf("should be disabled")
	}
	results, err := mgr.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(results) != 1 || !results[0].Loaded {
		t.Fatalf("Reload results: %+v", results)
	}
	// After reload, local auto-enabled again
	infos = mgr.Info()
	if !infos[0].Enabled {
		t.Fatalf("after Reload, local should be auto-enabled")
	}
}
