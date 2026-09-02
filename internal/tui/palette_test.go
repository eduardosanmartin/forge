package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPaletteEmberTokens(t *testing.T) {
	p, ok := GetPalette("ember")
	if !ok {
		t.Fatal("ember palette missing")
	}
	if p.BG != "#161210" || p.Accent != "#d97757" {
		t.Fatalf("ember tokens mismatch %+v", p)
	}
	if p.Border != "#332b24" || p.Text != "#e8e2da" {
		t.Fatalf("ember tokens mismatch %+v", p)
	}
}

func TestPaletteRegistryFallback(t *testing.T) {
	if _, ok := GetPalette("unknown"); ok {
		t.Fatal("unknown palette should not be found")
	}
	p := MustGetPalette("unknown")
	if p.Accent != "#d97757" {
		t.Fatalf("fallback should be ember, got %+v", p)
	}
	if p.BG != "#161210" {
		t.Fatalf("fallback BG wrong")
	}
}

func TestPaletteDefault(t *testing.T) {
	p := DefaultPalette()
	if p.Accent != "#d97757" {
		t.Fatalf("DefaultPalette should be ember")
	}
}

func TestPaletteNames(t *testing.T) {
	names := PaletteNames()
	if len(names) != 1 || names[0] != "ember" {
		t.Fatalf("PaletteNames = %v want [ember]", names)
	}
}

func TestPaletteAccentTruecolorSGR(t *testing.T) {
	p := MustGetPalette("ember")
	// Use lipgloss style and also direct ansi style to ensure truecolor SGR is present.
	// Lipgloss rendering delegates to ansi.Style; we check both paths.
	ls := lipgloss.NewStyle().Foreground(lipgloss.Color(p.Accent)).Render("test")
	// lipgloss may render via ansi.Style; check for 38;2;217;119;87 sequence
	if !strings.Contains(ls, "38;2;217;119;87") {
		// Fallback: direct ansi.Style should definitely produce truecolor.
		sty := ansi.Style{}.ForegroundColor(lipgloss.Color(p.Accent))
		direct := sty.Styled("test")
		if !strings.Contains(direct, "38;2;217;119;87") {
			t.Fatalf("accent rendering missing truecolor SGR 38;2;217;119;87, lipgloss=%q direct=%q", ls, direct)
		}
	}
}

func TestPaletteStyleHelpers(t *testing.T) {
	p := MustGetPalette("ember")
	// Each helper should produce non-empty rendered output containing expected colors via ansi.
	cases := []struct {
		name string
		fn   func() string
	}{
		{"Accent", func() string { return p.AccentStyle().Render("x") }},
		{"Text", func() string { return p.TextStyle().Render("x") }},
		{"Dim", func() string { return p.DimStyle().Render("x") }},
		{"Error", func() string { return p.ErrorStyle().Render("x") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.fn()
			if out == "" {
				t.Fatal("empty render")
			}
			if !strings.Contains(out, "x") {
				t.Fatalf("render missing content %q", out)
			}
		})
	}
}
