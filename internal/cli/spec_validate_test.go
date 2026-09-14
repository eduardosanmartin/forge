package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- Parser ----

func TestParseSpecRequirements(t *testing.T) {
	sectionRF1 := "RF-1. Núcleo de ejecución de agentes"
	sectionRNF4 := "RNF-4. Seguridad"
	table := []struct {
		name    string
		content string
		want    []SpecRequirement
	}{
		{
			name: "plain bullet with id",
			content: "### RF-1. Núcleo de ejecución de agentes\n" +
				"- RF-1.1 El sistema debe ejecutar un agente.\n",
			want: []SpecRequirement{{ID: "RF-1.1", Section: sectionRF1, Text: "RF-1.1 El sistema debe ejecutar un agente."}},
		},
		{
			name: "bold bullet strips markers",
			content: "### RF-4. Skills y auto-aprendizaje\n" +
				"- **RF-4.1.1 Debe proporcionar un wizard CLI interactivo.**\n",
			want: []SpecRequirement{{ID: "RF-4.1.1", Section: "RF-4. Skills y auto-aprendizaje", Text: "RF-4.1.1 Debe proporcionar un wizard CLI interactivo."}},
		},
		{
			name: "sub-id collected",
			content: "### RF-5. Plugins y extensibilidad\n" +
				"- RF-5.3 Debe existir un manifiesto de plugin.\n" +
				"- **RF-5.3.1 Debe proporcionar un wizard CLI.**\n",
			want: []SpecRequirement{
				{ID: "RF-5.3", Section: "RF-5. Plugins y extensibilidad", Text: "RF-5.3 Debe existir un manifiesto de plugin."},
				{ID: "RF-5.3.1", Section: "RF-5. Plugins y extensibilidad", Text: "RF-5.3.1 Debe proporcionar un wizard CLI."},
			},
		},
		{
			name: "narrative parenthetical section bullets are not requirements",
			// Mirrors the real "### 3.6 Wizard CLI" narrative block nested
			// between RF-6 and RF-7.
			content: "### RF-6. CLI\n" +
				"- RF-6.1 Modo no interactivo.\n" +
				"### 3.6 Wizard CLI para creación de plugins y skills\n" +
				"- **wizard genera SKILL.md válido:** nombre y frontmatter validados.\n" +
				"### RF-7. GUI web (opcional, desacoplada)\n" +
				"- RF-7.1 Modo servidor.\n",
			want: []SpecRequirement{
				{ID: "RF-6.1", Section: "RF-6. CLI", Text: "RF-6.1 Modo no interactivo."},
				{ID: "RF-7.1", Section: "RF-7. GUI web (opcional, desacoplada)", Text: "RF-7.1 Modo servidor."},
			},
		},
		{
			name: "non-requirement headings reset group context",
			content: "## 3. Arquitectura general\n" +
				"- RF-99.9 nowhere bullet.\n" +
				"### 3.1 Vista de alto nivel\n" +
				"- RF-99.8 still nowhere.\n" +
				"### RNF-4. Seguridad\n" +
				"- RNF-4.12 Exposición mínima de red.\n",
			want: []SpecRequirement{{ID: "RNF-4.12", Section: sectionRNF4, Text: "RNF-4.12 Exposición mínima de red."}},
		},
		{
			name: "wrong-section edge records heading as found",
			// Deliberately misplaced RNF bullet under an RF group: still a
			// requirement, with the section it actually sits under.
			content: "### RF-1. Núcleo de ejecución de agentes\n" +
				"- RNF-4.7 Aislamiento por SO.\n",
			want: []SpecRequirement{{ID: "RNF-4.7", Section: sectionRF1, Text: "RNF-4.7 Aislamiento por SO."}},
		},
		{
			name: "fenced code blocks are skipped entirely",
			content: "### RF-8. SDD\n" +
				"```\n" +
				"### RF-88. Fake heading inside fence\n" +
				"- RF-88.1 Fake requirement inside fence.\n" +
				"```\n" +
				"- RF-8.3 Divergence signals.\n",
			want: []SpecRequirement{{ID: "RF-8.3", Section: "RF-8. SDD", Text: "RF-8.3 Divergence signals."}},
		},
		{
			name: "ids mentioned mid-text are not requirements",
			content: "### RF-1. Núcleo\n" +
				"- El requisito RF-2.1 vive en otra sección.\n" +
				"- RF-1.2 requerimiento real.\n",
			want: []SpecRequirement{{ID: "RF-1.2", Section: "RF-1. Núcleo", Text: "RF-1.2 requerimiento real."}},
		},
		{
			name: "nested sub-bullets are never requirements",
			content: "### RF-11. Autonomía\n" +
				"- RF-11.2 El run manifest debe declarar:\n" +
				"  - RF-11.2.1 límites de autonomía.\n",
			want: []SpecRequirement{{ID: "RF-11.2", Section: "RF-11. Autonomía", Text: "RF-11.2 El run manifest debe declarar:"}},
		},
		{
			name:    "empty spec has no requirements",
			content: "",
			want:    []SpecRequirement{},
		},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSpecRequirements(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parse mismatch:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Integration guarded by invariants only: robust to spec evolution.
func TestParseSpecRequirementsRealSpecInvariants(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "spec-harness-agentic.md"))
	if err != nil {
		t.Fatalf("read real spec fixture: %v", err)
	}
	reqs := ParseSpecRequirements(string(content))
	if len(reqs) <= 50 {
		t.Fatalf("real spec must yield >50 requirement IDs, got %d", len(reqs))
	}
	ids := map[string]bool{}
	for _, r := range reqs {
		if r.Section == "" {
			t.Fatalf("requirement %q has empty section", r.ID)
		}
		if r.Text == "" {
			t.Fatalf("requirement %q has empty text", r.ID)
		}
		// Stray internal markdown bold is legitimate in long requirement
		// bodies; only surrounding top-level markers must be stripped.
		if strings.HasPrefix(r.Text, "**") || strings.HasSuffix(r.Text, "**") {
			t.Fatalf("requirement %q text not stripped of surrounding bold: %q", r.ID, r.Text)
		}
		if bulletRe.MatchString(r.Text) {
			t.Fatalf("requirement %q text still carries the bullet marker: %q", r.ID, r.Text)
		}
		ids[r.ID] = true
	}
	if len(ids) < len(reqs) {
		t.Fatalf("duplicate requirement IDs in real spec: %d unique of %d", len(ids), len(reqs))
	}
	// Spot-check structural invariants of the current fixture.
	for _, id := range []string{"RF-1.1", "RF-4.1.1", "RF-5.3.1", "RNF-4.12", "RF-8.3"} {
		if !ids[id] {
			t.Fatalf("real spec missing expected requirement %q", id)
		}
	}
}

// ---- Evidence scan ----

func writeTreeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestScanSpecEvidence(t *testing.T) {
	root := t.TempDir()
	writeTreeFile(t, filepath.Join(root, "main.go"), "package main\n// implements RF-1.1 and RNF-4.1\nfunc main() {}\n")
	writeTreeFile(t, filepath.Join(root, "internal", "core", "engine.go"), "package core\n// RF-2.1\n// stray mention without trailing dot RF-9.9.9\nvar _ = \"RNF-4.9\"\n")
	writeTreeFile(t, filepath.Join(root, "internal", "core", "engine_test.go"), "package core\n// RF-2.1 covered here too\n")
	writeTreeFile(t, filepath.Join(root, "vendor", "dep.go"), "package dep // RF-7.7 vendored\n")
	writeTreeFile(t, filepath.Join(root, "forge-plugins", "build.go"), "package plugin // RF-7.8 plugins\n")
	writeTreeFile(t, filepath.Join(root, ".forge", "marker.go"), "package state // RF-7.9 state\n")
	writeTreeFile(t, filepath.Join(root, ".git", "hook.go"), "package hook // RF-7.6 git\n")
	writeTreeFile(t, filepath.Join(root, "docs", "notes.md"), "RF-7.5 in markdown should not count\n")
	writeTreeFile(t, filepath.Join(root, "readme.txt"), "RF-7.4 in text\n")
	writeTreeFile(t, filepath.Join(root, "node_modules", "dep.go"), "package dep // RF-7.3 node modules\n")

	refs, err := scanSpecEvidence(root)
	if err != nil {
		t.Fatalf("scanSpecEvidence: %v", err)
	}
	type refKey struct{ f, id string }
	got := map[refKey][]int{}
	for _, r := range refs {
		got[refKey{r.File, r.ID}] = append(got[refKey{r.File, r.ID}], r.Line)
	}
	want := map[refKey][]int{
		{"main.go", "RF-1.1"}:                      {2},
		{"main.go", "RNF-4.1"}:                     {2},
		{"internal/core/engine.go", "RF-2.1"}:      {2},
		{"internal/core/engine.go", "RF-9.9.9"}:    {3},
		{"internal/core/engine.go", "RNF-4.9"}:     {4},
		{"internal/core/engine_test.go", "RF-2.1"}: {2},
	}
	if len(got) != len(want) {
		t.Fatalf("collected %+v (full refs: %+v), want keys %+v", got, refs, want)
	}
	for k, wantLines := range want {
		if !reflect.DeepEqual(got[k], wantLines) {
			t.Fatalf("%v = %v, want %v", k, got[k], wantLines)
		}
	}
}

// ---- Signals & state ----

func seedValidateFiles(t *testing.T, root string) {
	for rel, content := range specValidateFixtureFiles {
		writeTreeFile(t, filepath.Join(root, rel), content)
	}
}

// specContentFixture plus specValidateFixtureFiles are engineered so that:
//   - RF-1.1 and RF-2.1 have evidence (healthy, also claimed in skills_test.go),
//   - RF-3.1 has no references anywhere (req-without-evidence),
//   - RF-4.1.1 is a bold sub-ID requirement with evidence,
//   - fmt.go references RF-99.9 (evidence-without-req),
//   - skills_test.go additionally references RNF-88.8 (test files count as claims).
const specContentFixture = "# Spec\n" +
	"### RF-1. Núcleo\n" +
	"- RF-1.1 Ejecutar un agente.\n" +
	"### RF-2. Conectividad\n" +
	"- RF-2.1 Proveedor OpenAI-compat.\n" +
	"### RF-3. Memoria\n" +
	"- RF-3.1 Memoria persistente.\n" +
	"### RF-4. Skills\n" +
	"- **RF-4.1.1 Wizard CLI.**\n"

var specValidateFixtureFiles = map[string]string{
	"core.go":        "package main\n// core: RF-1.1 y RF-2.1\nvar x = 1\n",
	"skills.go":      "package main\n// skills wizard RF-4.1.1\nvar y = 1\n",
	"fmt.go":         "package main\n// fmt referencias RF-99.9\nvar z = 1\n",
	"skills_test.go": "package main\n// test claims RF-2.1 y RNF-88.8\n",
}

func TestBuildSpecValidateReportSignals(t *testing.T) {
	root := t.TempDir()
	writeSpecFixture(t, root, specContentFixture)
	seedValidateFiles(t, root)

	report, err := buildSpecValidateReport(root, "SPEC.md", specContentFixture)
	if err != nil {
		t.Fatalf("buildSpecValidateReport: %v", err)
	}

	if report.ChangeState != "first validation" {
		t.Fatalf("ChangeState = %q, want \"first validation\"", report.ChangeState)
	}
	if got := report.SpecSHA256; got != sha256Hex(specContentFixture) {
		t.Fatalf("SpecSHA256 = %q", got)
	}
	if report.RequirementCount != 4 {
		t.Fatalf("RequirementCount = %d, want 4 (RF-1.1, RF-2.1, RF-3.1, RF-4.1.1)", report.RequirementCount)
	}
	if report.EvidenceCount < 5 {
		t.Fatalf("EvidenceCount = %d, want >=5", report.EvidenceCount)
	}
	// req-without-evidence: RF-3.1 only.
	if len(report.ReqsWithoutEvidence) != 1 || report.ReqsWithoutEvidence[0].ID != "RF-3.1" {
		t.Fatalf("ReqsWithoutEvidence = %+v, want [RF-3.1]", report.ReqsWithoutEvidence)
	}
	// evidence-without-req: RF-99.9 and RNF-88.8 (dedup by file:line).
	ids := map[string]int{}
	for _, e := range report.EvidenceWithoutReq {
		ids[e.ID]++
	}
	if ids["RF-99.9"] != 1 || ids["RNF-88.8"] != 1 || len(report.EvidenceWithoutReq) != 2 {
		t.Fatalf("EvidenceWithoutReq = %+v, want one RF-99.9 and one RNF-88.8", report.EvidenceWithoutReq)
	}
	for _, e := range report.EvidenceWithoutReq {
		if e.File == "" || e.Line == 0 {
			t.Fatalf("evidence ref missing locator: %+v", e)
		}
	}
	if report.SemanticAuditNote == "" {
		t.Fatal(" SemanticAuditNote must document the follow-up")
	}
}

func writeSpecFixture(t *testing.T, root, content string) {
	t.Helper()
	writeTreeFile(t, filepath.Join(root, "SPEC.md"), content)
}

func TestSpecStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeSpecFixture(t, root, specContentFixture)
	seedValidateFiles(t, root)

	if _, err := buildSpecValidateReport(root, "SPEC.md", specContentFixture); err != nil {
		t.Fatalf("first validate: %v", err)
	}
	st, err := readSpecState(root)
	if err != nil || st == nil {
		t.Fatalf("state marker must exist after first validate: %v %v", st, err)
	}
	firstAt := st.ValidatedAt
	if firstAt == "" {
		t.Fatal("validated_at must be set")
	}
	if st.RequirementCount != 4 {
		t.Fatalf("marker RequirementCount = %d, want 4", st.RequirementCount)
	}

	report, err := buildSpecValidateReport(root, "SPEC.md", specContentFixture)
	if err != nil {
		t.Fatalf("second validate: %v", err)
	}
	if report.ChangeState != "unchanged" {
		t.Fatalf("second run ChangeState = %q, want \"unchanged\"", report.ChangeState)
	}

	mutated := specContentFixture + "- RF-3.2 Retrieval selectivo.\n"
	report, err = buildSpecValidateReport(root, "SPEC.md", mutated)
	if err != nil {
		t.Fatalf("third validate: %v", err)
	}
	if report.ChangeState != "changed-since-last-validate" {
		t.Fatalf("mutated run ChangeState = %q, want \"changed-since-last-validate\"", report.ChangeState)
	}
	st, _ = readSpecState(root)
	if st.SpecSHA256 != sha256Hex(mutated) {
		t.Fatal("marker must persist the new spec hash after mutation")
	}
}

func TestReadSpecStateFirstValidationWithoutMarker(t *testing.T) {
	root := t.TempDir()
	st, err := readSpecState(root)
	if err != nil {
		t.Fatalf("missing marker must not error: %v", err)
	}
	if st != nil {
		t.Fatalf("want nil state, got %+v", st)
	}
}

// ---- Command & JSON envelope ----

func TestSpecValidateCommandConstruction(t *testing.T) {
	cmd := newSpecCommand()
	if _, _, err := cmd.Find([]string{"validate"}); err != nil {
		t.Fatalf("spec must expose validate subcommand: %v", err)
	}
	v := newSpecValidateCommand()
	if f := v.Flags().Lookup("json"); f == nil {
		t.Fatal("spec validate must expose --json")
	}
	if f := v.Flags().Lookup("path"); f == nil {
		t.Fatal("spec validate must expose --path")
	}
	// Inventory report, not a gate: no positional args restricted and no
	// validator set (cobra treats a nil Args as unrestricted).
	if v.Args != nil {
		t.Fatal("spec validate must not restrict positional args (always-runnable inventory)")
	}
	for _, phrase := range []string{"req-without-evidence", "evidence-without-req", "follow-up", "exits 0"} {
		if !strings.Contains(v.Long, phrase) {
			t.Fatalf("validate help must document %q", phrase)
		}
	}
}

func execSpecValidateCommand(t *testing.T, root string, args []string) []byte {
	t.Helper()
	t.Chdir(root)
	buf := &bytes.Buffer{}
	cmd := newSpecValidateCommand()
	cmd.SetArgs(args)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("spec validate %v: %v", args, err)
	}
	return buf.Bytes()
}

func TestSpecValidateJSONEnvelopeAndHumanOutput(t *testing.T) {
	root := t.TempDir()
	writeSpecFixture(t, root, specContentFixture)
	seedValidateFiles(t, root)

	// Second validate run: state marker present, so ChangeState = unchanged.
	first, err := buildSpecValidateReport(root, "SPEC.md", specContentFixture)
	if err != nil {
		t.Fatalf("warm state marker: %v", err)
	}
	_ = first

	out := execSpecValidateCommand(t, root, []string{"--json"})
	var env struct {
		OK      bool               `json:"ok"`
		Command string             `json:"command"`
		Result  SpecValidateReport `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Command != "spec validate" {
		t.Fatalf("envelope = ok:%v command:%q", env.OK, env.Command)
	}
	r := env.Result
	if r.Path != "SPEC.md" || r.ChangeState != "unchanged" {
		t.Fatalf("envelope path/change = %q/%q", r.Path, r.ChangeState)
	}
	if r.SpecSHA256 != sha256Hex(specContentFixture) {
		t.Fatal("envelope hash mismatch")
	}
	if len(r.ReqsWithoutEvidence) != 1 || r.ReqsWithoutEvidence[0].ID != "RF-3.1" {
		t.Fatalf("envelope req-without-evidence = %+v", r.ReqsWithoutEvidence)
	}
	if r.SemanticAuditNote == "" {
		t.Fatal("envelope must carry the semantic-audit follow-up note")
	}

	// Human output: three signal sections + footer note.
	human := execSpecValidateCommand(t, root, []string{})
	text := string(human)
	for _, want := range []string{
		"req-without-evidence: 1",
		"evidence-without-req: 2",
		"spec state signal: unchanged",
		"RF-3.1",
		"RF-99.9",
		semanticAuditNote,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("human output missing %q:\n%s", want, text)
		}
	}
}

func TestSpecValidatePathFlagResolution(t *testing.T) {
	root := t.TempDir()
	someSpec := "# Spec\n### RF-6. CLI\n- RF-6.3 JSON y no-json.\n"
	writeSpecFixture(t, root, "other.md")
	writeTreeFile(t, filepath.Join(root, "custom", "spec2.md"), someSpec)

	// Reuses PR-A path resolution: explicit --path wins.
	out := execSpecValidateCommand(t, root, []string{"--path", "custom/spec2.md", "--json"})
	var env struct {
		Result SpecValidateReport `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if env.Result.Path != filepath.ToSlash("custom/spec2.md") {
		t.Fatalf("path = %q, want custom/spec2.md", env.Result.Path)
	}
}

func TestSpecValidateNeverMutatesRepo(t *testing.T) {
	// Only the .forge/spec-state.json marker may be written; no git artifacts.
	root := t.TempDir()
	writeSpecFixture(t, root, specContentFixture)
	seedValidateFiles(t, root)
	if _, err := buildSpecValidateReport(root, "SPEC.md", specContentFixture); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".forge", "spec-state.json")); err != nil {
		t.Fatalf("state marker must exist at .forge/spec-state.json: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case ".forge", "SPEC.md", "core.go", "skills.go", "fmt.go", "skills_test.go":
		default:
			t.Fatalf("validate created unexpected entry %q (inventory must not mutate the repo)", e.Name())
		}
	}
}

func TestRunSpecValidateMissingSpecErrorsWithoutFiles(t *testing.T) {
	// No context App, empty cwd probe target: resolution failure surfaces
	// cleanly (consistent with spec log/show behavior).
	cmd := newSpecValidateCommand()
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runSpecValidate(cmd, &buf, "", false); err == nil {
		t.Fatal("want resolution error when no spec file exists")
	} else if !strings.Contains(err.Error(), "spec file not found") {
		t.Fatalf("err = %v, want missing-spec resolution error", err)
	}
}
