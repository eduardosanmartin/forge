// Code moved verbatim from model.go (M12 split) — same package, no behavior change.

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// renderTitleBar renders the M2 full-width title bar: "forge <tui-version> ·
// daemon <daemon-version>". Rendered as a bordered box in the same style as
// the footer bar (NormalBorder + BGElevated) so the version is always
// visible as top chrome. The box is EXACTLY 3 rows (top border, content,
// bottom border) — SetSize and handleMouseClick account for titleHeightRows.
// cwd is deliberately NOT shown here: components.FooterModel already
// renders it on the footer's left side, and repeating it here was pure
// duplication (reported live — the footer already has the same data).
func (m Model) renderTitleBar() string {
	ver := tuiVersion()
	daemonVer := m.daemonVers
	if daemonVer == "" {
		daemonVer = "dev"
	}
	left := fmt.Sprintf("forge %s · daemon %s", ver, daemonVer)
	width := m.width
	if width <= 0 {
		width = 80
	}
	// Box Width includes the borders: cap the inner bar to the content area
	// (width-2) so it can never wrap to a second row and break the frame.
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	bar := ansi.Truncate(left, inner, "…")
	content := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Text)).
		Render(bar)
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.palette.Border)).
		Background(lipgloss.Color(m.palette.BGElevated)).
		Width(width).
		Render(content)
}

func (m Model) renderSeparator() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	line := strings.Repeat("─", w)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render(line)
}

func (m Model) renderSuggestions() string {
	if !m.suggestionsVisible || len(m.suggestions) == 0 {
		return ""
	}
	pal := m.palette
	styleSel := lipgloss.NewStyle().Background(lipgloss.Color(pal.BGElevated)).Foreground(lipgloss.Color(pal.Accent)).Bold(true)
	styleNorm := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text))
	styleDim := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim))
	styleBorder := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(pal.Border)).Background(lipgloss.Color(pal.BGElevated)).Padding(0, 1)
	var lines []string
	for i, s := range m.suggestions {
		cmdStr := s.Command
		desc := s.Description
		line := cmdStr + "  " + styleDim.Render(desc)
		if i == m.suggestionIdx {
			line = styleSel.Render("▶ "+cmdStr) + " " + styleNorm.Render(desc)
		} else {
			line = styleNorm.Render("  "+cmdStr) + " " + styleDim.Render(desc)
		}
		lines = append(lines, line)
	}
	inner := strings.Join(lines, "\n")
	return styleBorder.Width(m.effectiveTranscriptWidth()).Render(inner)
}

// View renders M2 "Status Rail" layout: title bar (full-width, top) +
// transcript (left) + rail (right, 32 cols, three cards) + accent separator
// (full-width) above the input + input + footer.
//
// Mouse hit-testing (TUI-7): zones are derived in handleMouseClick from the
// SAME constants this function renders with (title row 1, viewport height,
// input 4, measured footer height). Documented fragility: rail card zones are
// equal thirds of the rail column and footer hotspots are fixed right-edge
// bands, so both drift if card content or footer composition changes; the
// pure zone functions (HitTestRail, HitTestFooter) are covered by tests.
func (m Model) View() tea.View {
	// Self-heal the chrome-height budget (viewport/input/footer split) for
	// whatever m.toast/overlay-visibility state is ACTUALLY about to be
	// rendered below — see SetSize's doc comment. m is a local copy (value
	// receiver), so relayout() (pointer receiver, called on the addressable
	// local m) only affects this one render; the real model is untouched.
	m.relayout()
	// Title bar (full-width, top)
	titleBar := m.renderTitleBar()
	// Transcript as rendered (the animated spinner lives only in the
	// footer bar; the pending message itself carries the Working marker
	// with its tick-stamped elapsed, wrapped safely inside its bubble).
	transcriptView := m.viewport.View()
	inputView := m.input.View()
	if m.suggestionsVisible {
		suggView := m.renderSuggestions()
		if suggView != "" {
			inputView = suggView + "\n" + inputView
		}
	}
	if m.sessionsDropdownVisible {
		focusHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ session focus: ↑/↓ navigate · enter select · n new session · esc/ctrl+g close")
		inputView = focusHint + "\n" + inputView
	} else if m.modelPanelVisible {
		mpHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ model select: ↑/↓ navigate · enter select · esc close")
		inputView = mpHint + "\n" + inputView
	} else if m.sessionFocus {
		focusHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ session focus: ↑/↓ navigate · enter switch · n new session · esc/ctrl+g back")
		inputView = focusHint + "\n" + inputView
	}
	compPal := toCompPalette(m.palette)
	showMoreBelow := false
	if len(m.entries) > 0 && !m.viewport.AtBottom() {
		showMoreBelow = true
	}
	footerHint := ""
	if m.sessionsDropdownVisible || m.sessionFocus {
		footerHint = "session focus"
	} else if m.modelPanelVisible {
		footerHint = "model select"
	} else if m.runPanelVisible {
		footerHint = "run panel"
	} else if m.railPanel != "" {
		footerHint = m.railPanel + " panel"
	}
	if m.railPanel != "" && footerHint == "" {
		footerHint = m.railPanel
	}
	if !m.mouseCapture {
		if footerHint != "" {
			footerHint += " · mouse off"
		} else {
			footerHint = "mouse off"
		}
	}
	footer := components.FooterModel{
		Palette:       compPal,
		Width:         m.width,
		Cwd:           m.cwd,
		SessionID:     m.sessionID,
		DaemonAddr:    m.daemonAddr,
		Toast:         m.toast,
		DaemonErr:     m.daemonErr,
		ShowSpinner:   m.spinner,
		SpinnerView:   m.spinnerModel.View(),
		WorkingStats:  m.lastWorkingElapsed,
		Layout:        m.footerLayoutLabel(),
		ModelName:     m.currentModel,
		Tokens:        m.totalTokens,
		ShowMoreBelow: showMoreBelow,
		FocusHint:     footerHint,
	}.Render()
	// Separator above input (accent)
	separator := m.renderSeparator()
	// Sidebar / rail data (M2: same three cards as TUI-6 sidebar, now in permanent rail)
	avgLatencyStr := ""
	if m.latencyCount > 0 {
		avgMs := m.latencyTotalMs / int64(m.latencyCount)
		if avgMs < 1000 {
			avgLatencyStr = fmt.Sprintf("%dms", avgMs)
		} else {
			avgLatencyStr = fmt.Sprintf("%.1fs", float64(avgMs)/1000)
		}
	}
	turnsInWindow := len(m.entries)
	sidebarData := components.SidebarData{
		Palette:       compPal,
		TotalTokens:   m.totalTokens,
		TurnsInWindow: turnsInWindow,
		TurnCount:     m.turnCount,
		AvgLatency:    avgLatencyStr,
		LastError:     m.lastError,
		Plugins:       m.plugins,
		Skills:        m.skills,
	}
	// railHeight MUST equal the transcript viewport's own height exactly —
	// no independent floor. A prior version floored this at 5 while the
	// viewport itself floors at 3 (SetSize); at terminal heights where the
	// computed transcript height landed in [3,5), the rail (always exactly
	// railHeight tall via RenderColumnCapped) ended up taller than the
	// transcript, lipgloss.JoinHorizontal stretched the whole main row to
	// match it, and the extra rows pushed the footer past the bottom of the
	// frame (visible as "the footer moves down" when the rail is shown).
	// Small terminals now degrade by truncating the rail's card content
	// more aggressively (RenderColumnCapped already handles that) instead
	// of breaking the frame's total height.
	railHeight := m.viewport.Height()
	sidebar := components.SidebarModel{
		Data:   sidebarData,
		Width:  railWidth,
		Height: railHeight,
	}
	// Main row: transcript + optional rail. RenderColumnCapped hard-caps the
	// rail box to EXACTLY railHeight rows (lipgloss Height is only a minimum,
	// so uncapped card content would overflow and clip the footer).
	var mainRow string
	if m.showSidebar {
		railView := sidebar.RenderColumnCapped()
		mainRow = lipgloss.JoinHorizontal(lipgloss.Top, transcriptView, railView)
	} else {
		mainRow = transcriptView
	}
	content := strings.Join([]string{titleBar, mainRow, separator, inputView, footer}, "\n")
	// Floating overlays render OVER the bottom rows of the main row so they
	// stay on screen: anything appended below the frame is cut off live and
	// invisible. The frame keeps exactly terminal height; the overlay covers
	// transcript rows (keyboard-driven; esc closes).
	maxOverlayRows := len(strings.Split(mainRow, "\n"))
	if maxOverlayRows < 3 {
		maxOverlayRows = 3
	}
	float := func(box string) {
		mainRow = floatOverlay(mainRow, box, maxOverlayRows)
		content = strings.Join([]string{titleBar, mainRow, separator, inputView, footer}, "\n")
	}
	if m.helpVisible {
		helpLines := strings.Split(m.helpViewport.View(), "\n")
		helpCap := maxOverlayRows - 4 // borders + padding
		if helpCap < 1 {
			helpCap = 1
		}
		if len(helpLines) > helpCap {
			helpLines = helpLines[:helpCap]
		}
		helpStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Border)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(1, 1)
		float(helpStyle.Render(strings.Join(helpLines, "\n")))
	}
	if m.sessionsDropdownVisible {
		dropdown := m.renderSessionsDropdown(maxOverlayRows)
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(dropdown))
	}
	if m.modelPanelVisible {
		panel := m.renderModelPanel(maxOverlayRows)
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(panel))
	}
	if m.railPanel != "" {
		var panel string
		switch m.railPanel {
		case "context":
			panel = m.renderContextPanel()
		case "plugins":
			panel = m.renderPluginsPanel()
		case "turnstats":
			panel = m.renderTurnStatsPanel()
		}
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(capLines(panel, maxOverlayRows-2)))
	}
	if m.runPanelVisible {
		panel := m.renderRunPanel()
		borderColor := m.palette.Accent
		if m.runResult != nil && m.runResult.PendingCheckpoint != nil {
			// A pending checkpoint needs a decision — the accent-vs-warning
			// border distinguishes "just watching progress" from "blocked,
			// awaiting you" at a glance, same idea as the message panel's
			// error/success border color swap.
			borderColor = m.palette.Warning
		}
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(borderColor)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(capLines(panel, maxOverlayRows-2)))
	}
	// Message panel (error or success confirmation) renders LAST (highest
	// z-order among floats — see also its esc-priority in tea.KeyPressMsg)
	// so it's never hidden behind another panel open at the same time.
	// Narrower than the other overlays and centered (see
	// messagePanelWidth) — a short confirmation/error reads like a focused
	// notification, not another full-screen panel.
	if m.msgPanelVisible {
		content, color := m.renderMessagePanel()
		msgStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(color)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.messagePanelWidth()).Padding(0, 2)
		box := msgStyle.Render(capLines(content, maxOverlayRows-2))
		centered := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, box,
			lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BG))))
		float(centered)
	}
	if len(m.permQueue) > 0 {
		float(m.renderPermissionPrompt())
	}
	bg := lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BG)).Foreground(lipgloss.Color(m.palette.Text)).Width(m.width).Height(m.height).Render(content)
	v := tea.NewView(bg)
	v.AltScreen = true
	if m.mouseCapture {
		v.MouseMode = tea.MouseModeCellMotion
	} else {
		v.MouseMode = 0
	}
	return v
}

// floatOverlay places a pre-built overlay box over the bottom rows of the
// main row (transcript area), keeping the frame exactly terminal height.
// boxLines longer than maxRows fall back to replacing the whole area
// (shouldn't happen: callers cap content before boxing so borders survive).
func floatOverlay(mainRow, box string, maxRows int) string {
	bl := strings.Split(box, "\n")
	if len(bl) > maxRows {
		bl = bl[:maxRows]
	}
	ml := strings.Split(mainRow, "\n")
	if len(bl) >= len(ml) {
		return strings.Join(bl, "\n")
	}
	copy(ml[len(ml)-len(bl):], bl)
	return strings.Join(ml, "\n")
}

// capLines hard-caps text to max rows so a later box keeps intact borders.
func capLines(s string, max int) string {
	if max < 1 {
		max = 1
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:max-1], "\n") + "\n" + "…"
}

func (m Model) renderModelPanel(maxRows int) string {
	if len(m.modelPanelList) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no models)")
	}
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Select Model")

	rows := make([]modelPanelRow, 0, len(m.modelPanelList)+4)
	lastProvider := ""
	cursorRow := 0
	for i, e := range m.modelPanelList {
		if e.Provider != lastProvider {
			rows = append(rows, modelPanelRow{text: e.Provider, entryIdx: -1})
			lastProvider = e.Provider
		}
		if i == m.modelPanelIdx {
			cursorRow = len(rows)
		}
		rows = append(rows, modelPanelRow{text: e.Model, entryIdx: i})
	}

	// Window by RENDERED ROW (header rows count too, so a group heading
	// never scrolls away from the entries it labels), keyed to the
	// cursor's row rather than its entry index.
	budget := maxRows - 5
	if budget < 1 {
		budget = 1
	}
	start, end := windowRange(len(rows), cursorRow, budget)
	if len(m.modelPanelList) > budget {
		title += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(
			fmt.Sprintf(" (%d/%d)", m.modelPanelIdx+1, len(m.modelPanelList)))
	}
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("plugin-providers not listed yet (deferred)") + "\n")
	for r := start; r < end; r++ {
		row := rows[r]
		if row.entryIdx == -1 {
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Bold(true).Render(row.text) + "\n")
			continue
		}
		e := m.modelPanelList[row.entryIdx]
		line := "    " + e.Model
		if e.Model == m.currentModel {
			line = "  ● " + e.Model
		}
		if row.entryIdx == m.modelPanelIdx {
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("  ▶ " + e.Model)
		} else {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(line)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("↑/↓ navigate · enter select · esc close"))
	return sb.String()
}

func (m Model) renderContextPanel() string {
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Context & tokens")
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(fmt.Sprintf("session tokens: %s", formatTokens(m.totalTokens))) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("turns in window: %d", len(m.entries))) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("current model: %s", m.currentModel)) + "\n")
	if m.currentModel == "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(model not set)") + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("window % unavailable via RPC — shows local turns") + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

func (m Model) renderPluginsPanel() string {
	var sb strings.Builder
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Plugins & skills")
	sb.WriteString(title + "\n")
	if len(m.plugins) == 0 && len(m.skills) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no plugins/skills)") + "\n")
	} else {
		for _, p := range m.plugins {
			status := "disabled"
			if p.Enabled {
				status = "enabled"
			}
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(p.Name) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("["+status+"]") + "\n")
		}
		for _, s := range m.skills {
			status := "disabled"
			if s.Enabled {
				status = "enabled"
			}
			line := textStyle.Render(s.Name) + " " + dimStyle.Render("["+status+"]")
			if s.Category != "" {
				line += " " + dimStyle.Render("("+s.Category+")")
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("enable/disable via: forge plugin/skill CLI") + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

func (m Model) renderTurnStatsPanel() string {
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Turn stats")
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("turns: %d", m.turnCount)) + "\n")
	avg := "—"
	if m.latencyCount > 0 {
		avgMs := m.latencyTotalMs / int64(m.latencyCount)
		if avgMs < 1000 {
			avg = fmt.Sprintf("%dms", avgMs)
		} else {
			avg = fmt.Sprintf("%.1fs", float64(avgMs)/1000)
		}
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("avg latency: ") + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(avg) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("last errors (last 5):") + "\n")
	// Show lastError (single truncated last error) and toast history? For now show lastError only, plus count.
	if m.lastError != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  • "+truncateError(m.lastError, 60)) + "\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  —") + "\n")
	}
	if m.toast != "" && m.toast != m.lastError {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  • "+truncateError(m.toast, 60)) + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

// renderMessagePanel builds the floating message panel's content (see
// showError/showSuccess) and the border color to pair with it: a bold
// heading colored by kind (red "Error" / green "Confirmado"), the FULL
// untruncated message — word-wrapped to the panel's content width via
// lipgloss's own Width-based wrapping, not the footer toast's hard
// truncation — and an "esc to close" hint. Every segment sets its own
// Background explicitly: lipgloss's Width-triggered wrapping does NOT
// inherit a background from an outer style once applied — found as a real
// bug where wrapped lines rendered with the terminal's default background
// (usually black) instead of the panel's BGElevated. innerWidth must match
// the caller's box Width/Padding in View() (2 border cols + 2×2 padding
// cols = 6) or the wrap width and the box width drift.
func (m Model) renderMessagePanel() (content, borderColor string) {
	innerWidth := m.messagePanelWidth() - 6
	if innerWidth < 10 {
		innerWidth = 10
	}
	heading, color := "Error", m.palette.Error
	if m.msgPanelKind == messagePanelSuccess {
		heading, color = "Confirmado", m.palette.Success
	}
	bg := lipgloss.Color(m.palette.BGElevated)
	title := lipgloss.NewStyle().Bold(true).Background(bg).Foreground(lipgloss.Color(color)).Render(heading)
	body := lipgloss.NewStyle().Width(innerWidth).Background(bg).Foreground(lipgloss.Color(m.palette.Text)).Render(m.msgPanelText)
	hint := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(m.palette.Faint)).Render("esc to close")
	return title + "\n" + body + "\n" + hint, color
}
