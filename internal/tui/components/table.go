package components

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

// tableAlign is a column's alignment, taken from its markdown separator
// cell (":---" left, "---:" right, ":---:" center, "---" default/left).
type tableAlign int

const (
	alignLeft tableAlign = iota
	alignCenter
	alignRight
)

// tableSepCellRe matches one separator-row cell: dashes with optional
// leading/trailing colons for alignment (GFM table syntax).
var tableSepCellRe = regexp.MustCompile(`^:?-{1,}:?$`)

// splitTableRow splits one markdown table row into trimmed cells. Handles
// both `| a | b |` and the bare `a | b` form (no outer pipes) — models
// don't always emit the outer pipes consistently.
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// isTableSeparatorRow reports whether line is a GFM table separator row
// (the line of dashes/colons right after the header, e.g. "|---|:--:|").
func isTableSeparatorRow(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" || !tableSepCellRe.MatchString(c) {
			return false
		}
	}
	return true
}

func cellAlignFromSeparator(cell string) tableAlign {
	left := strings.HasPrefix(cell, ":")
	right := strings.HasSuffix(cell, ":")
	switch {
	case left && right:
		return alignCenter
	case right:
		return alignRight
	default:
		return alignLeft
	}
}

// findTableAt checks whether a GFM table starts at lines[i]: a header row
// containing "|" immediately followed by a valid separator row with the
// same column count. On success it keeps consuming subsequent "|"-bearing,
// non-blank lines as data rows (padding/truncating each to the header's
// column count — a model's own table sometimes drops a trailing empty
// cell) until one doesn't qualify.
func findTableAt(lines []string, i int) (rows [][]string, aligns []tableAlign, consumed int, ok bool) {
	if i+1 >= len(lines) {
		return nil, nil, 0, false
	}
	header, sep := lines[i], lines[i+1]
	if !strings.Contains(header, "|") || !isTableSeparatorRow(sep) {
		return nil, nil, 0, false
	}
	headerCells := splitTableRow(header)
	sepCells := splitTableRow(sep)
	if len(headerCells) == 0 || len(sepCells) != len(headerCells) {
		return nil, nil, 0, false
	}
	aligns = make([]tableAlign, len(sepCells))
	for j, c := range sepCells {
		aligns[j] = cellAlignFromSeparator(c)
	}
	rows = [][]string{headerCells}
	consumed = 2
	for i+consumed < len(lines) {
		line := lines[i+consumed]
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "|") {
			break
		}
		row := splitTableRow(line)
		for len(row) < len(headerCells) {
			row = append(row, "")
		}
		if len(row) > len(headerCells) {
			row = row[:len(headerCells)]
		}
		rows = append(rows, row)
		consumed++
	}
	return rows, aligns, consumed, true
}

// padCell pads s with spaces to exactly w visible columns per align.
func padCell(s string, w int, align tableAlign) string {
	gap := w - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	switch align {
	case alignRight:
		return strings.Repeat(" ", gap) + s
	case alignCenter:
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
	default:
		return s + strings.Repeat(" ", gap)
	}
}

// renderMarkdownTable renders a parsed table (rows[0] is the header) as a
// box-drawn, column-aligned block, shrinking columns proportionally when
// the natural widths don't fit avail — a table always renders at a
// consistent width, never wrapped mid-row (which would destroy the grid).
func renderMarkdownTable(rows [][]string, aligns []tableAlign, avail int, pal Palette) []string {
	if len(rows) == 0 {
		return nil
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for c, cell := range row {
			if w := lipgloss.Width(cell); w > widths[c] {
				widths[c] = w
			}
		}
	}

	// Cap any single column's natural width before fitting to avail: an
	// unbounded long-prose column (a "Descripción" cell with a full
	// sentence) would otherwise either blow the table past avail entirely
	// or, once proportionally shrunk below, squash every OTHER column down
	// with it. Capped columns still show their full text — via wrapping
	// below, not truncation — just across more lines instead of one very
	// wide one.
	const maxColWidth = 32
	for c := range widths {
		if widths[c] > maxColWidth {
			widths[c] = maxColWidth
		}
	}

	// Overhead: cols+1 vertical borders, plus 2 padding columns per cell.
	overhead := (cols + 1) + cols*2
	total := overhead
	for _, w := range widths {
		total += w
	}
	if total > avail && avail > overhead {
		budget := avail - overhead
		sumW := 0
		for _, w := range widths {
			sumW += w
		}
		if sumW > 0 {
			for c := range widths {
				widths[c] = widths[c] * budget / sumW
				if widths[c] < 6 {
					widths[c] = 6
				}
			}
		}
	}

	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Border))
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Bold(true)
	cellStyle := pal.TextStyle()

	hline := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for c, w := range widths {
			b.WriteString(strings.Repeat("─", w+2))
			if c < len(widths)-1 {
				b.WriteString(mid)
			}
		}
		b.WriteString(right)
		return borderStyle.Render(b.String())
	}

	// renderRow wraps (never truncates) each cell's content to its column
	// width and returns one physical line per the TALLEST wrapped cell in
	// the row — reported live: cells were being cut off with "…" instead of
	// showing their full content, which this replaces.
	renderRow := func(row []string, st lipgloss.Style) []string {
		cellLines := make([][]string, cols)
		height := 1
		for c, w := range widths {
			wrapped := lipgloss.Wrap(row[c], w, "")
			cellLines[c] = strings.Split(wrapped, "\n")
			if len(cellLines[c]) > height {
				height = len(cellLines[c])
			}
		}
		lines := make([]string, height)
		for li := 0; li < height; li++ {
			var b strings.Builder
			b.WriteString(borderStyle.Render("│"))
			for c, w := range widths {
				var cellLine string
				if li < len(cellLines[c]) {
					cellLine = cellLines[c][li]
				}
				cellLine = padCell(cellLine, w, aligns[c])
				b.WriteString(" ")
				b.WriteString(st.Render(cellLine))
				b.WriteString(" ")
				b.WriteString(borderStyle.Render("│"))
			}
			lines[li] = b.String()
		}
		return lines
	}

	out := []string{hline("┌", "┬", "┐")}
	out = append(out, renderRow(rows[0], headerStyle)...)
	out = append(out, hline("├", "┼", "┤"))
	for _, row := range rows[1:] {
		out = append(out, renderRow(row, cellStyle)...)
	}
	out = append(out, hline("└", "┴", "┘"))
	return out
}

// renderProseWithTables scans plain (non-code-fence) content for GFM
// tables, rendering each as a box-drawn grid and everything else exactly
// as before (wrapPlain + styledLines) — the two never mix within a single
// rendered line, so a table stays a table even when surrounded by prose.
func renderProseWithTables(part string, avail int, pal Palette) []string {
	lines := strings.Split(part, "\n")
	var out []string
	var textBuf []string
	flush := func() {
		if len(textBuf) == 0 {
			return
		}
		joined := strings.Join(textBuf, "\n")
		if strings.TrimSpace(joined) != "" {
			out = append(out, styledLines(wrapPlain(joined, avail), pal.TextStyle())...)
		}
		textBuf = textBuf[:0]
	}
	for i := 0; i < len(lines); {
		if rows, aligns, consumed, ok := findTableAt(lines, i); ok {
			flush()
			out = append(out, renderMarkdownTable(rows, aligns, avail, pal)...)
			i += consumed
			continue
		}
		textBuf = append(textBuf, lines[i])
		i++
	}
	flush()
	return out
}
