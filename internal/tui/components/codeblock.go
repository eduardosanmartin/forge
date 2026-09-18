package components

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// tokenStyle maps a chroma token family to a lipgloss style built from the
// active Palette — not a hardcoded chroma style — so syntax colors follow
// whichever theme is active (ember, etc.) instead of introducing a second,
// independent color scheme. Every category also carries the elevated
// background so the gutter and every wrapped continuation row still read
// as one continuous code block, matching the flat-tint look this replaces.
//
// LiteralString/LiteralNumber and NameFunction/NameBuiltin are checked with
// InSubCategory, not InCategory: chroma.TokenType.InCategory divides by
// 1000 (the top-level family — Literal, Name, ...), and LiteralString
// (3100) and LiteralNumber (3200) both round down to the SAME top-level
// Literal category, so InCategory(LiteralString) would also match every
// number token. InSubCategory divides by 100, which is what actually tells
// these sibling subtypes apart. Keyword and Comment are checked with
// InCategory deliberately: nothing else shares their top-level thousands
// bucket, so the broader, simpler check is correct there and also catches
// every keyword/comment subtype (constant, declaration, preproc, ...).
func tokenStyle(tt chroma.TokenType, pal Palette) lipgloss.Style {
	bg := lipgloss.Color(pal.BGElevated)
	switch {
	case tt.InCategory(chroma.Comment):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Faint)).Background(bg).Italic(true)
	case tt.InSubCategory(chroma.LiteralString):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Success)).Background(bg)
	case tt.InSubCategory(chroma.LiteralNumber):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Warning)).Background(bg)
	case tt.InCategory(chroma.Keyword):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Background(bg).Bold(true)
	case tt.InSubCategory(chroma.NameFunction), tt.InSubCategory(chroma.NameBuiltin):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Background(bg)
	case tt == chroma.Error || tt.InCategory(chroma.GenericError):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Error)).Background(bg)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text)).Background(bg)
	}
}

// lexerFor resolves a chroma lexer for lang (a fenced-code-block language
// tag), falling back to content analysis when lang is empty or unrecognized,
// and finally to the plaintext lexer so highlighting never errors out —
// worst case, unrecognized code just renders in the default text color.
func lexerFor(lang, body string) chroma.Lexer {
	if lang != "" {
		if l := lexers.Get(lang); l != nil {
			return l
		}
	}
	if l := lexers.Analyse(body); l != nil {
		return l
	}
	return lexers.Fallback
}

// highlightLines tokenizes body with lexer and reassembles it into one
// fully-styled (ANSI-colored) string PER SOURCE LINE. Splitting each
// token's value on "\n" before styling — rather than styling the whole
// (possibly multi-line) token and splitting the rendered result — matters:
// lipgloss.Style.Render on a string containing embedded newlines closes
// with a single trailing reset, which would leave every line after the
// first with no closing SGR once the caller later splits that on "\n" for
// wrapping. Styling each line-segment independently keeps every line
// self-contained (its own open + reset) no matter how it's split or
// wrapped afterward.
func highlightLines(lexer chroma.Lexer, body string, pal Palette) []string {
	tokens, err := chroma.Tokenise(lexer, nil, body)
	if err != nil {
		tokens = []chroma.Token{{Type: chroma.Text, Value: body}}
	}
	var lines []string
	var cur strings.Builder
	for _, tok := range tokens {
		st := tokenStyle(tok.Type, pal)
		segs := strings.Split(tok.Value, "\n")
		for i, seg := range segs {
			if seg != "" {
				cur.WriteString(st.Render(seg))
			}
			if i < len(segs)-1 {
				lines = append(lines, cur.String())
				cur.Reset()
			}
		}
	}
	lines = append(lines, cur.String())
	return lines
}

// renderHighlightedCodeBlock renders a fenced code block's body with
// per-token syntax coloring plus a right-aligned line-number gutter,
// wrapped to avail columns. body must already have its surrounding blank
// lines trimmed (the caller does this once before computing line numbers).
func renderHighlightedCodeBlock(lang, body string, avail int, pal Palette) []string {
	lexer := lexerFor(lang, body)
	lines := highlightLines(lexer, body, pal)

	numWidth := len(strconv.Itoa(len(lines)))
	const sep = " │ "
	gutterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Faint)).Background(lipgloss.Color(pal.BGElevated))
	gutterWidth := numWidth + len(sep)
	contentWidth := avail - gutterWidth
	if contentWidth < 10 {
		contentWidth = 10
	}

	padStyle := lipgloss.NewStyle().Background(lipgloss.Color(pal.BGElevated))

	var out []string
	for i, line := range lines {
		wrapped := lipgloss.Wrap(line, contentWidth, "")
		for j, row := range strings.Split(wrapped, "\n") {
			var gutter string
			if j == 0 {
				gutter = gutterStyle.Render(fmt.Sprintf("%*d%s", numWidth, i+1, sep))
			} else {
				gutter = gutterStyle.Render(strings.Repeat(" ", numWidth) + sep)
			}
			full := gutter + row
			// Each token's background only covers its own glyphs, so a line
			// shorter than avail leaves the rest of the row uncolored —
			// visually a "ragged" block instead of a solid one (reported
			// after trying this live in `forge tui`). Pad the row out to
			// avail with background-only spaces so the block reads as one
			// continuous colored rectangle, right edge included.
			if w := lipgloss.Width(full); w < avail {
				full += padStyle.Render(strings.Repeat(" ", avail-w))
			}
			out = append(out, full)
		}
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}
