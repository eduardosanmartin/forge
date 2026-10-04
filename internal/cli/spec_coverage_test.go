package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The SPEC's coverage header used to be edited by hand and drifted from the
// checklist under it (it still said "13 parciales" with 3 left). This test
// recounts the checklist and fails when the header disagrees.
func TestSpecCoverageHeaderMatchesChecklist(t *testing.T) {
	spec := readSpec(t)
	start := strings.Index(spec, "## Estado de cobertura de requerimientos")
	if start < 0 {
		t.Fatal("coverage section not found")
	}
	end := strings.Index(spec[start+10:], "\n## ")
	section := spec[start : start+10+end]

	var done, partial, pending int
	for _, m := range regexp.MustCompile(`(?m)^- \[([ x~])\] (?:RF|RNF)-\d`).FindAllStringSubmatch(section, -1) {
		switch m[1] {
		case "x":
			done++
		case "~":
			partial++
		default:
			pending++
		}
	}
	total := done + partial + pending

	h := regexp.MustCompile(`\*\*(\d+)/(\d+) cubiertos\*\*, (\d+) parciales \(` + "`- \\[~\\]`" + `\), (\d+) pendientes`).FindStringSubmatch(section)
	if h == nil {
		t.Fatal("coverage header line not found (expected **N/M cubiertos**, K parciales (`- [~]`), P pendientes)")
	}
	got := []int{atoi(t, h[1]), atoi(t, h[2]), atoi(t, h[3]), atoi(t, h[4])}
	want := []int{done, total, partial, pending}
	for i, name := range []string{"cubiertos", "total", "parciales", "pendientes"} {
		if got[i] != want[i] {
			t.Errorf("header says %d %s, the checklist has %d", got[i], name, want[i])
		}
	}
	if n := regexp.MustCompile(`de los (\d+) requerimientos`).FindStringSubmatch(section); n == nil || atoi(t, n[1]) != total {
		t.Errorf("intro count %v, the checklist has %d requirements", n, total)
	}
}

// The SPEC was once double-encoded (UTF-8 read as cp1252): 496 lines of
// "VisiÃ³n"-style text, which also blinded the §3.7 lexical flag scan.
func TestSpecIsCleanUTF8(t *testing.T) {
	for i, line := range strings.Split(readSpec(t), "\n") {
		if strings.Contains(line, "Ã") || strings.Contains(line, "â€") || strings.ContainsRune(line, '�') {
			t.Errorf("line %d looks mis-encoded: %.80q", i+1, line)
		}
	}
}

func readSpec(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec-harness-agentic.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
