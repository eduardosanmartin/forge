package components

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	ansistrip "github.com/charmbracelet/x/ansi"
)

// TestBuildContentTableAligned is the regression lock for the reported bug:
// a markdown table in an assistant reply must render as a column-aligned
// grid, not raw pipe-delimited text run through the plain word-wrapper
// (which breaks alignment the moment any cell's width differs).
func TestBuildContentTableAligned(t *testing.T) {
	pal := testPalette()
	content := "Resultado:\n\n" +
		"| Nombre | Edad | Ciudad |\n" +
		"|---|---:|:---:|\n" +
		"| Ana | 30 | BA |\n" +
		"| Guillermo | 7 | Rosario |\n" +
		"\nListo."
	entries := []Entry{{Role: "assistant", Content: content}}
	out := BuildContent(entries, pal, 80)
	plain := ansistrip.Strip(out)

	lines := strings.Split(plain, "\n")
	var tableLines []string
	for _, ln := range lines {
		if strings.ContainsAny(ln, "\u2502\u250c\u2514\u251c") {
			tableLines = append(tableLines, ln)
		}
	}
	// top border, header, header/body divider, row 1, row divider, row 2, bottom border.
	if len(tableLines) != 7 {
		t.Fatalf("expected 7 table lines (borders+header+divider+2 rows), got %d:\n%s", len(tableLines), strings.Join(tableLines, "\n"))
	}

	// Every table line must be the exact same visible width — that's what
	// "aligned" means; the raw pipe-text version would NOT satisfy this
	// (different cell content makes different-length raw lines).
	want := lipgloss.Width(tableLines[0])
	for i, ln := range tableLines {
		if w := lipgloss.Width(ln); w != want {
			t.Errorf("table line %d width = %d, want %d (misaligned): %q", i, w, want, ln)
		}
	}

	for _, want := range []string{"Nombre", "Edad", "Ciudad", "Ana", "30", "BA", "Guillermo", "Rosario"} {
		if !strings.Contains(plain, want) {
			t.Errorf("table content lost: missing %q in:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "Resultado:") || !strings.Contains(plain, "Listo.") {
		t.Errorf("surrounding prose lost:\n%s", plain)
	}
}

// TestBuildContentTableSeparatesEveryDataRow is the regression lock for a
// real bug reported live: the header/body divider rendered fine, but
// consecutive DATA rows ran together with nothing marking where one row
// ended and the next began. Every data row must get its own divider line
// below it (except the last, which gets the bottom border instead).
func TestBuildContentTableSeparatesEveryDataRow(t *testing.T) {
	pal := testPalette()
	content := "| Modelo | Empresa | Contexto |\n" +
		"|---|---|---|\n" +
		"| A | Nvidia | 128k |\n" +
		"| B | Anthropic | 200k |\n" +
		"| C | OpenAI | 400k |\n"
	entries := []Entry{{Role: "assistant", Content: content}}
	out := BuildContent(entries, pal, 80)
	plain := ansistrip.Strip(out)

	lines := strings.Split(plain, "\n")
	var dividers int
	for _, ln := range lines {
		if strings.Contains(ln, "\u251c") { // "├"
			dividers++
		}
	}
	// 1 header/body divider + 2 between the 3 data rows = 3.
	if dividers != 3 {
		t.Fatalf("expected 3 divider lines (header/body + 2 between 3 data rows), got %d:\n%s", dividers, plain)
	}
}

// TestFindTableAtRejectsNonTables checks the detector doesn't fire on
// ordinary prose containing a literal "|" or a markdown horizontal rule.
func TestFindTableAtRejectsNonTables(t *testing.T) {
	lines := []string{"esto no es una tabla", "solo texto normal"}
	if _, _, _, ok := findTableAt(lines, 0); ok {
		t.Error("plain prose without a separator row should not be detected as a table")
	}

	lines2 := []string{"a | b", "---"}
	if rows, _, _, ok := findTableAt(lines2, 0); ok {
		t.Errorf("mismatched column counts (1 header cols=2, sep cols=1) should be rejected, got %v", rows)
	}
}

func TestPadCellAlignment(t *testing.T) {
	if got := padCell("x", 5, alignLeft); got != "x    " {
		t.Errorf("left align = %q", got)
	}
	if got := padCell("x", 5, alignRight); got != "    x" {
		t.Errorf("right align = %q", got)
	}
	if got := padCell("x", 5, alignCenter); got != "  x  " {
		t.Errorf("center align = %q", got)
	}
}

// TestBuildContentTableWrapsLongCellsInsteadOfTruncating is the regression
// lock for a real bug reported live: a long cell ("Requiere Node instalado;
// c\u00f3digo compacto\u2026") was being cut off with an ellipsis. Long cells must now
// wrap across multiple physical lines within the same logical row \u2014 full
// content always reaches the screen, just taller.
func TestBuildContentTableWrapsLongCellsInsteadOfTruncating(t *testing.T) {
	pal := testPalette()
	longText := "Requiere Node instalado; el c\u00f3digo queda compacto pero necesita configuraci\u00f3n extra"
	content := "| Opci\u00f3n | Descripci\u00f3n |\n" +
		"|---|---|\n" +
		"| A | " + longText + " |\n"
	entries := []Entry{{Role: "assistant", Content: content}}
	out := BuildContent(entries, pal, 60)
	plain := ansistrip.Strip(out)

	if strings.Contains(plain, "\u2026") {
		t.Errorf("table content should wrap, not truncate with an ellipsis, got:\n%s", plain)
	}
	for _, word := range strings.Fields(longText) {
		if !strings.Contains(plain, word) {
			t.Errorf("wrapped cell lost word %q, got:\n%s", word, plain)
		}
	}

	lines := strings.Split(plain, "\n")
	var tableLines []string
	for _, ln := range lines {
		if strings.ContainsAny(ln, "\u2502\u250c\u2514\u251c") {
			tableLines = append(tableLines, ln)
		}
	}
	if len(tableLines) <= 5 {
		t.Errorf("expected the long cell's row to span multiple physical lines, got %d table lines:\n%s", len(tableLines), strings.Join(tableLines, "\n"))
	}
	want := lipgloss.Width(tableLines[0])
	for i, ln := range tableLines {
		if w := lipgloss.Width(ln); w != want {
			t.Errorf("table line %d width = %d, want %d (misaligned): %q", i, w, want, ln)
		}
	}
}
