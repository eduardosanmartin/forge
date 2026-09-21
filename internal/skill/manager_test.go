package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/embedding"
)

func writeSkillMD(t *testing.T, dir string, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestManager_Scan_HappyPathLocalAutoEnabled(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "code-review-style")
	writeSkillMD(t, skillDir, "---\nname: code-review-style\ndescription: \"Provides guidance for code reviews\"\nsource: local\n---\nInstructions body\n")
	// create script if declared? Not declared here, so no script needed.

	mgr := NewManager(Options{})
	defer mgr.Close()

	results, err := mgr.Scan(root)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(results) != 1 || !results[0].Loaded {
		t.Fatalf("expected 1 loaded, got %+v", results)
	}
	loaded := mgr.Loaded()
	if len(loaded) != 1 || loaded[0] != "code-review-style" {
		t.Errorf("Loaded = %v", loaded)
	}
	enabled := mgr.Enabled()
	if len(enabled) != 1 || enabled[0] != "code-review-style" {
		t.Errorf("Enabled = %v, want auto-enabled local", enabled)
	}
}

func TestManager_Scan_ExternalWithoutApprovalFailClosed(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "ext-skill")
	// checksum will be valid but approval false -> fail
	content := "---\nname: ext-skill\ndescription: \"external skill\"\nsource: external\nchecksum: \"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n---\nBody\n"
	writeSkillMD(t, skillDir, content)

	mgr := NewManager(Options{ApproveExternal: false})
	defer mgr.Close()
	results, err := mgr.Scan(root)
	if err == nil {
		t.Fatal("expected scan error for external without approval")
	}
	if len(results) != 1 || results[0].Loaded {
		t.Errorf("external without approval should not be loaded: %+v", results)
	}
	if !errors.Is(results[0].Err, ErrApprovalRequired) && !strings.Contains(results[0].Err.Error(), "approval") {
		t.Errorf("Err should be approval required, got %v", results[0].Err)
	}
	if len(mgr.Loaded()) != 0 {
		t.Errorf("Loaded should be empty, got %v", mgr.Loaded())
	}
}

func TestManager_Scan_ExternalWithValidChecksumAndApprove(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "ext-skill")
	// Prepare content and compute hash WITHOUT the checksum line (manager strips it).
	baseContent := "---\nname: ext-skill\ndescription: \"external skill valid\"\nsource: external\nchecksum: \"CHECKSUM_PLACEHOLDER\"\n---\nBody\n"
	tmpContent := strings.Replace(baseContent, "CHECKSUM_PLACEHOLDER", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
	// Compute hash of content stripped of checksum line.
	cleaned := stripChecksumLineForTest([]byte(tmpContent))
	sum := sha256.Sum256(cleaned)
	hexSum := hex.EncodeToString(sum[:])
	correctChecksum := "sha256:" + hexSum
	finalContent := strings.Replace(baseContent, "CHECKSUM_PLACEHOLDER", correctChecksum, 1)
	writeSkillMD(t, skillDir, finalContent)

	mgr := NewManager(Options{ApproveExternal: true})
	defer mgr.Close()
	results, err := mgr.Scan(root)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(results) != 1 || !results[0].Loaded {
		t.Fatalf("expected loaded, got %+v err %v", results, err)
	}
	// External should be loaded but NOT enabled
	if len(mgr.Enabled()) != 0 {
		t.Errorf("external approved should be loaded-not-enabled, got enabled %v", mgr.Enabled())
	}
	if len(mgr.Loaded()) != 1 {
		t.Errorf("Loaded = %v", mgr.Loaded())
	}
	// Enable should work
	if err := mgr.Enable("ext-skill"); err != nil {
		t.Fatalf("Enable failed: %v", err)
	}
	if len(mgr.Enabled()) != 1 {
		t.Errorf("after Enable, enabled = %v", mgr.Enabled())
	}
}

func TestManager_Scan_ExternalWrongChecksumFailClosed(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "ext-skill")
	content := "---\nname: ext-skill\ndescription: \"external\"\nsource: external\nchecksum: \"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"\n---\nBody\n"
	writeSkillMD(t, skillDir, content)
	mgr := NewManager(Options{ApproveExternal: true})
	defer mgr.Close()
	results, err := mgr.Scan(root)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if results[0].Loaded {
		t.Error("should not be loaded on checksum mismatch")
	}
	if !errors.Is(err, ErrChecksumMismatch) && !strings.Contains(results[0].Err.Error(), "checksum") {
		// The aggregated error may wrap, check result Err
		if !strings.Contains(results[0].Err.Error(), "checksum") {
			t.Errorf("expected checksum error, got %v", results[0].Err)
		}
	}
}

func TestManager_EnableDisableTransitionsAndSentinels(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "my-skill")
	writeSkillMD(t, skillDir, "---\nname: my-skill\ndescription: \"desc\"\nsource: local\n---\nBody\n")
	mgr := NewManager(Options{})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Local auto-enabled, so Enable should fail with AlreadyEnabled
	if err := mgr.Enable("my-skill"); !errors.Is(err, ErrAlreadyEnabled) {
		t.Errorf("Enable on already enabled should be ErrAlreadyEnabled, got %v", err)
	}
	// Disable works
	if err := mgr.Disable("my-skill"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(mgr.Enabled()) != 0 {
		t.Errorf("after Disable, enabled = %v", mgr.Enabled())
	}
	// Double disable -> ErrNotEnabled
	if err := mgr.Disable("my-skill"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("second Disable should be ErrNotEnabled, got %v", err)
	}
	// Enable after disable works
	if err := mgr.Enable("my-skill"); err != nil {
		t.Fatalf("Enable after disable: %v", err)
	}
	// Enable non-existent -> ErrNotLoaded
	if err := mgr.Enable("nope"); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("Enable non-loaded should be ErrNotLoaded, got %v", err)
	}
	// Disable non-existent -> ErrNotLoaded
	if err := mgr.Disable("nope"); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("Disable non-loaded should be ErrNotLoaded, got %v", err)
	}
}

func TestManager_LoadedEnabledSorted(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z-skill", "a-skill", "m-skill"} {
		dir := filepath.Join(root, name)
		writeSkillMD(t, dir, "---\nname: "+name+"\ndescription: \"desc\"\nsource: local\n---\nBody\n")
	}
	mgr := NewManager(Options{})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	loaded := mgr.Loaded()
	expected := []string{"a-skill", "m-skill", "z-skill"}
	if !equalStringSlices(loaded, expected) {
		t.Errorf("Loaded sorted = %v want %v", loaded, expected)
	}
	enabled := mgr.Enabled()
	if !equalStringSlices(enabled, expected) {
		t.Errorf("Enabled sorted = %v want %v", enabled, expected)
	}
	// Check sorting after enabling subset? All locals are enabled already.
	if !sort.StringsAreSorted(loaded) {
		t.Error("Loaded not sorted")
	}
}

func TestManager_Relevant_MatchingVsUnrelated(t *testing.T) {
	root := t.TempDir()
	desc := "Provides guidance for code reviews and pull request style checks"
	skillDir := filepath.Join(root, "code-review-style")
	writeSkillMD(t, skillDir, "---\nname: code-review-style\ndescription: \""+desc+"\"\nsource: local\nactivation_keywords: [\"code review\", \"style\", \"PR\"]\n---\nSkill instructions body\n")
	otherDir := filepath.Join(root, "gardening-tips")
	writeSkillMD(t, otherDir, "---\nname: gardening-tips\ndescription: \"Advice for gardening and cooking recipes\"\nsource: local\n---\nGardening body\n")

	mgr := NewManager(Options{MinScore: 0.4, TopK: 1})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Realistic paraphrase sharing vocabulary (not exact description) — the point of token embeddings.
	paraphrase := "Please review this code for style issues and PR feedback"
	hits, err := mgr.Relevant(paraphrase)
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for paraphrase query sharing words")
	}
	if hits[0].Name != "code-review-style" {
		t.Errorf("top hit = %q want code-review-style", hits[0].Name)
	}

	// Exact match must also hit (1.0).
	combined := desc + " code review style PR"
	hits, err = mgr.Relevant(combined)
	if err != nil {
		t.Fatalf("Relevant exact: %v", err)
	}
	if len(hits) == 0 || hits[0].Name != "code-review-style" {
		t.Errorf("exact match should hit code-review-style, got %v err %v", hits, err)
	}

	// Unrelated query with no shared content words should return none (score ~0.0 vs threshold 0.3)
	unrelated := "quantum entanglement photon galaxy astronomy"
	hits, err = mgr.Relevant(unrelated)
	if err != nil {
		t.Fatalf("Relevant unrelated: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("unrelated query should return no skills, got %v", hits)
	}
}

func TestManager_Relevant_TopKAndMinScore(t *testing.T) {
	root := t.TempDir()
	descA := "quantum entanglement photon experiment"
	descB := "culinary knife sharpening techniques"
	writeSkillMD(t, filepath.Join(root, "a-skill"), "---\nname: a-skill\ndescription: \""+descA+"\"\nsource: local\n---\nBodyA\n")
	writeSkillMD(t, filepath.Join(root, "b-skill"), "---\nname: b-skill\ndescription: \""+descB+"\"\nsource: local\n---\nBodyB\n")

	mgr := NewManager(Options{MinScore: 0.4, TopK: 1})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	hits, _ := mgr.Relevant(descA)
	if len(hits) != 1 || hits[0].Name != "a-skill" {
		t.Errorf("TopK=1 should return only best match, got %v", hits)
	}

	mgr2 := NewManager(Options{MinScore: 0.4, TopK: 2})
	defer mgr2.Close()
	if _, err := mgr2.Scan(root); err != nil {
		t.Fatalf("Scan2: %v", err)
	}
	// With token embeddings, only identical scores 1.0, other ~0, so TopK 2 still returns 1.
	hits, _ = mgr2.Relevant(descA)
	if len(hits) != 1 {
		t.Errorf("expected 1 hit with TopK 2 but MinScore filters, got %v", hits)
	}
}

func TestManager_CloseIdempotent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "my-skill")
	writeSkillMD(t, dir, "---\nname: my-skill\ndescription: \"desc\"\nsource: local\n---\nBody\n")
	mgr := NewManager(Options{})
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("second Close should be idempotent, got %v", err)
	}
	if len(mgr.Loaded()) != 0 || len(mgr.Enabled()) != 0 {
		t.Errorf("after Close, should be empty, got loaded %v enabled %v", mgr.Loaded(), mgr.Enabled())
	}
}

func TestManager_Scan_MissingDirNotError(t *testing.T) {
	mgr := NewManager(Options{})
	defer mgr.Close()
	results, err := mgr.Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not be error, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results should be empty, got %v", results)
	}
}

// TestManager_ScanAll_ProjectOverridesGlobalOnNameCollision covers Fase 3
// of hojaDeRuta-embeddings-skills.md's precedence rule: a skill present in
// both roots with the same name resolves to the project's copy — the
// global entry is skipped, not an error, not merged.
func TestManager_ScanAll_ProjectOverridesGlobalOnNameCollision(t *testing.T) {
	projectRoot := t.TempDir()
	globalRoot := t.TempDir()

	writeSkillMD(t, filepath.Join(projectRoot, "shared-name"),
		"---\nname: shared-name\ndescription: \"project version\"\nsource: local\n---\nPROJECT VERSION BODY\n")
	writeSkillMD(t, filepath.Join(globalRoot, "shared-name"),
		"---\nname: shared-name\ndescription: \"global version\"\nsource: local\n---\nGLOBAL VERSION BODY\n")
	writeSkillMD(t, filepath.Join(globalRoot, "global-only"),
		"---\nname: global-only\ndescription: \"only in global\"\nsource: local\n---\nGLOBAL ONLY BODY\n")

	mgr := NewManager(Options{})
	defer mgr.Close()
	results, err := mgr.ScanAll(projectRoot, globalRoot)
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}

	// shared-name must appear exactly once in results (the project load),
	// never a second time from the global pass.
	shareCount := 0
	for _, r := range results {
		if r.Name == "shared-name" {
			shareCount++
		}
	}
	if shareCount != 1 {
		t.Fatalf("shared-name should be loaded exactly once (project wins), got %d results", shareCount)
	}

	loaded := mgr.Loaded()
	sort.Strings(loaded)
	wantLoaded := []string{"global-only", "shared-name"}
	if len(loaded) != len(wantLoaded) || loaded[0] != wantLoaded[0] || loaded[1] != wantLoaded[1] {
		t.Fatalf("Loaded() = %v, want %v", loaded, wantLoaded)
	}

	info := mgr.Info()
	byName := make(map[string]SkillInfo, len(info))
	for _, i := range info {
		byName[i.Name] = i
	}
	if byName["shared-name"].Description != "project version" {
		t.Errorf("shared-name description = %q, want the project version, not global's", byName["shared-name"].Description)
	}
	if byName["shared-name"].Origin != string(OriginProject) {
		t.Errorf("shared-name Origin = %q, want %q", byName["shared-name"].Origin, OriginProject)
	}
	if byName["global-only"].Origin != string(OriginGlobal) {
		t.Errorf("global-only Origin = %q, want %q", byName["global-only"].Origin, OriginGlobal)
	}
}

// TestManager_ActiveManual_ProjectListPlusGlobalUnion covers the manual
// activation set directly at the Manager level (context_skills_test.go in
// internal/agent covers the same thing end to end through Build()).
func TestManager_ActiveManual_ProjectListPlusGlobalUnion(t *testing.T) {
	projectRoot := t.TempDir()
	globalRoot := t.TempDir()

	writeSkillMD(t, filepath.Join(projectRoot, "listed"),
		"---\nname: listed\ndescription: \"listed in project config\"\nsource: local\n---\nLISTED BODY\n")
	writeSkillMD(t, filepath.Join(projectRoot, "unlisted"),
		"---\nname: unlisted\ndescription: \"not listed\"\nsource: local\n---\nUNLISTED BODY\n")
	writeSkillMD(t, filepath.Join(globalRoot, "always-on"),
		"---\nname: always-on\ndescription: \"global\"\nsource: local\n---\nALWAYS ON BODY\n")

	mgr := NewManager(Options{})
	defer mgr.Close()
	if _, err := mgr.ScanAll(projectRoot, globalRoot); err != nil {
		t.Fatalf("ScanAll: %v", err)
	}

	active := mgr.ActiveManual([]string{"listed"})
	names := make(map[string]bool, len(active))
	for _, sk := range active {
		names[sk.Name] = true
	}
	if !names["listed"] {
		t.Error("listed (in configEnabled) should be active")
	}
	if names["unlisted"] {
		t.Error("unlisted (not in configEnabled, not global) should not be active")
	}
	if !names["always-on"] {
		t.Error("always-on (global) should be active without being listed")
	}
}

// TestManager_Relevant_CachesAcrossRepeatedCalls is a regression lock for
// Fase 1 of hojaDeRuta-embeddings-skills.md: Relevant() is called once per
// agent tool-calling iteration within a single turn (internal/agent/loop.go
// calls ContextAssembler.Build, which calls Relevant, inside the turn's
// iteration loop) — with the SAME userMessage and the SAME enabled skills
// every time. Before the fix, N iterations against M enabled skills meant
// N*(1+M) embedding computations for identical text; after it, the
// underlying embedding.Store's cache holds exactly 1 (query) + M (skills)
// entries no matter how many times Relevant is called.
func TestManager_Relevant_CachesAcrossRepeatedCalls(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, filepath.Join(root, "skill-a"),
		"---\nname: skill-a\ndescription: \"first skill for cache regression test\"\nsource: local\n---\nBODY A\n")
	writeSkillMD(t, filepath.Join(root, "skill-b"),
		"---\nname: skill-b\ndescription: \"second skill for cache regression test\"\nsource: local\n---\nBODY B\n")

	mgr := NewManager(Options{MinScore: 0.0})
	defer mgr.Close()
	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	const query = "same query repeated across every iteration of one turn"
	const simulatedIterations = 5
	for i := 0; i < simulatedIterations; i++ {
		if _, err := mgr.Relevant(query); err != nil {
			t.Fatalf("Relevant call %d: %v", i, err)
		}
	}

	store := mgr.embedStore
	if store == nil {
		t.Fatal("manager has no embed store")
	}
	// 1 query + 2 skill descriptions (each combined with its — here empty —
	// activation keywords) = 3 distinct texts, regardless of
	// simulatedIterations.
	if got := store.GenCacheSize(); got != 3 {
		t.Fatalf("embed cache size after %d repeated Relevant() calls = %d, want 3 (1 query + 2 skills, not %d*(1+2)=%d)",
			simulatedIterations, got, simulatedIterations, simulatedIterations*3)
	}
}

// TestManager_Reload_PreservesInjectedStore is a regression lock for a real
// bug found while implementing Fase 4 of hojaDeRuta-embeddings-skills.md:
// resetStateLocked (called by every Scan/ScanAll/Reload) used to
// unconditionally recreate m.embedStore as a fresh hash-only store,
// silently discarding a real backend passed via Options.Store — a
// Manager configured with a real embedding backend would quietly revert
// to the bag-of-words hash after its very first Reload, with no error or
// log line. This test builds a Manager with a distinguishable custom
// store and confirms the SAME store instance is still wired after Reload.
func TestManager_Reload_PreservesInjectedStore(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, filepath.Join(root, "skill-a"),
		"---\nname: skill-a\ndescription: \"skill for reload regression test\"\nsource: local\n---\nBODY\n")

	customStore, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mgr := NewManager(Options{Store: customStore})
	defer mgr.Close()

	if _, err := mgr.Scan(root); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if mgr.embedStore != customStore {
		t.Fatal("Scan replaced the injected store on its first call")
	}

	if _, err := mgr.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if mgr.embedStore != customStore {
		t.Fatal("Reload replaced the injected store — the exact bug this test locks against")
	}
}

// TestManager_Close_DoesNotCloseExternallyOwnedStore covers the ownership
// split needed once skill.Manager can share a Store with something else
// (internal/cli/daemon.go shares one between skills and retrieval, Fase 4
// of hojaDeRuta-embeddings-skills.md): Close() must only close a store it
// created itself. embedding.Store.Close() is a harmless no-op today, so
// this can't be observed via a panic or error — it asserts on Manager's
// own bookkeeping (ownsStore) instead, which is what actually decides the
// behavior.
func TestManager_Close_DoesNotCloseExternallyOwnedStore(t *testing.T) {
	external, err := embedding.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mgr := NewManager(Options{Store: external})
	if mgr.ownsStore {
		t.Fatal("ownsStore should be false when Options.Store was supplied")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	owned := NewManager(Options{})
	if !owned.ownsStore {
		t.Fatal("ownsStore should be true when Options.Store was nil (Manager created its own)")
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func stripChecksumLineForTest(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "checksum:") {
			continue
		}
		kept = append(kept, l)
	}
	return []byte(strings.Join(kept, "\n"))
}
