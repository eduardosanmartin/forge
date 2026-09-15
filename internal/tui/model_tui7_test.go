package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// ---------- 1. M2 Layout: title bar, rail, separator, footer ----------

func TestM2_TitleBarAndSeparatorPresent(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.sessionID = "sess-xyz"
	m.daemonVers = "v3.0.0"
	m.cwd = "/tmp/test-cwd"
	m.rebuildTranscript()
	view := m.View().Content
	if !strings.Contains(view, "forge") {
		t.Fatalf("title bar should contain 'forge', view %q", view[:500])
	}
	if !strings.Contains(view, m.cwd) && !strings.Contains(view, "test-cwd") {
		t.Fatalf("title bar should contain cwd %q", m.cwd)
	}
	if !strings.Contains(view, "daemon") {
		t.Fatalf("title bar should contain daemon version")
	}
	if !strings.Contains(view, "─") {
		t.Fatalf("separator line (─) should be present above input")
	}
}

func TestM2_RailOnOffDistinct(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.entries = []components.Entry{{Role: "user", Content: "hello"}}
	m.rebuildTranscript()
	m.showSidebar = true
	on := m.View().Content
	m.showSidebar = false
	off := m.View().Content
	if on == off {
		t.Fatal("rail on and off views must be distinct")
	}
	if !strings.Contains(on, "Context & tokens") {
		t.Fatalf("rail on should contain Context & tokens")
	}
	// rail off should be wider transcript (no rail border) but still have title/separator
	if !strings.Contains(off, "forge") {
		t.Fatal("rail off should still have title bar")
	}
}

func TestM2_CtrlOAndCtrlLToggleRail(t *testing.T) {
	m := newTestModel()
	initial := m.ShowSidebar()
	// ctrl+o toggles
	model, _ := m.Update(keyPress("ctrl+o"))
	mm := model.(Model)
	if mm.ShowSidebar() == initial {
		t.Fatalf("ctrl+o should toggle rail")
	}
	// No toast: the footer's Layout field already shows "rail on"/"rail
	// off" persistently — a toast here would duplicate that same text
	// right below it on the same frame (the bug this test now guards
	// against).
	if mm.Toast() != "" {
		t.Fatalf("toggling the rail should not set a toast (footer already shows layout state), got %q", mm.Toast())
	}
	// ctrl+l alias toggles back
	model, _ = mm.Update(keyPress("ctrl+l"))
	mm = model.(Model)
	if mm.ShowSidebar() != initial {
		t.Fatalf("ctrl+l alias should toggle rail back")
	}
}

// ---------- 2. Footer height fix: small terminal ----------

func TestFooterHeight_NoClippingAtSmallHeight(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 20)
	m.entries = []components.Entry{{Role: "user", Content: "hello"}}
	m.rebuildTranscript()
	view := m.View().Content
	// Footer should be fully within view; last line should be border bottom
	if !strings.Contains(view, "┌") || !strings.Contains(view, "┘") && !strings.Contains(view, "└") {
		// Footer uses NormalBorder, check for border chars
		t.Logf("footer border not found but check view")
	}
	// Height accounting: transcript + title + separator + input + footer == terminal height within 2 lines tolerance
	lines := strings.Count(view, "\n") + 1
	if lines > 22 {
		t.Fatalf("view lines %d exceeds terminal height 20 significantly (clipping), view lines %d", lines, lines)
	}
	// Measure footer height dynamically
	fh := m.measureFooterHeight(80)
	if fh < 2 {
		t.Fatalf("footer height %d too small", fh)
	}
	// Mirror SetSize accounting: title box + separator + input + measured
	// footer + 1 toast-appearance headroom (no toast shown). No pending
	// headroom: the spinner lives inside the footer bar.
	transH := 20 - titleHeightRows - fh - 1 - 4 - 1
	if transH < 5 {
		transH = 5
	}
	if m.viewport.Height() != transH {
		t.Fatalf("transcript height %d want %d (footer %d)", m.viewport.Height(), transH, fh)
	}
}

// ---------- 3. Suggestion interceptor fix ----------

func TestSuggestionInterceptor_ModelExactSends(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0755)
	_ = os.WriteFile(cfgPath, []byte(`{"providers":{"p1":{"kind":"openai-compatible","base_url":"http://a/v1","models":["m1"]}}}`), 0644)
	m := newTestModel()
	m.configPath = cfgPath
	m.SetSize(80, 24)
	m.currentModel = "m1"
	// Type "/model" exactly and press enter => should open panel (send), not just complete
	m.input.SetValue("/model")
	m.updateSuggestions() // trigger suggestion visibility like Update would
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if !mm.IsModelPanelVisible() {
		t.Fatalf("/model + enter should open model panel (send), suggestions intercept bug not fixed, toast %q", mm.Toast())
	}
	if mm.input.Value() != "" {
		t.Fatalf("input should be cleared after slash send")
	}
}

func TestSuggestionInterceptor_PrefixCompletes(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.input.SetValue("/lay")
	m.updateSuggestions()
	if !m.IsSuggestionsVisible() {
		t.Fatal("suggestions should be visible for /lay")
	}
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if mm.IsSuggestionsVisible() {
		t.Fatal("after complete, suggestions should be hidden")
	}
	val := mm.input.Value()
	if !strings.HasPrefix(val, "/layout") {
		t.Fatalf("/lay + enter should complete to /layout, got %q", val)
	}
	// Should NOT have sent (no panel, not cleared to toast? input not empty, no execute)
	if val == "" {
		t.Fatal("complete should leave input with /layout, not clear")
	}
}

func TestSuggestionInterceptor_HelpSends(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.input.SetValue("/help")
	m.updateSuggestions()
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if !mm.IsHelpVisible() {
		t.Fatalf("/help + enter should execute (open help), got toast %q", mm.Toast())
	}
}

// ---------- 4. Mouse capture toggle ----------

func TestMouseCaptureToggle(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	// Default is capture ON (wheel scroll + click hotspots work).
	if !m.IsMouseCapture() {
		t.Fatal("default mouse capture should be on")
	}
	view := m.View()
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("View MouseMode should be CellMotion by default, got %v", view.MouseMode)
	}
	model, _ := m.Update(keyPress("ctrl+m"))
	mm := model.(Model)
	if mm.IsMouseCapture() {
		t.Fatal("ctrl+m should toggle off for text selection")
	}
	if !strings.Contains(mm.Toast(), "mouse capture off") {
		t.Fatalf("toast should mention mouse capture off, got %q", mm.Toast())
	}
	view2 := mm.View()
	if view2.MouseMode != 0 {
		t.Fatalf("View MouseMode should be 0 when capture off, got %v", view2.MouseMode)
	}
	// Toggle back on
	model, _ = mm.Update(keyPress("ctrl+m"))
	mm = model.(Model)
	if !mm.IsMouseCapture() {
		t.Fatal("second ctrl+m should toggle on")
	}
	if !strings.Contains(mm.Toast(), "mouse capture on") {
		t.Fatalf("toast should mention mouse capture on, got %q", mm.Toast())
	}
}

// ---------- 5. Elapsed + tokens surviving reload (sidecar) ----------

func TestSidecar_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".forge", "tui-state.json")
	m := newTestModel()
	m.sidecarPath = path
	m.sidecar = make(map[string]int64)
	m.sessionID = "sess-abc"
	// Simulate turn completion with duration
	m.clock = newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m.turnStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fc := newFakeClock(time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)) // 2s elapsed
	m.SetClock(fc)
	m.currentModel = "glm-5.3-flash"
	// Execute turn result with seq 5 assistant
	res := &daemon.ExecuteTurnResult{
		Model: "glm-5.3-flash",
		Messages: []daemon.MessageResult{
			{Seq: 5, Role: "assistant", Content: "hello", Usage: &daemon.UsageResult{TotalTokens: 100}},
		},
	}
	model, _ := m.Update(executeTurnMsg{res: res, err: nil})
	mm := model.(Model)
	// Sidecar should have entry
	key := sidecarKey("sess-abc", 5)
	if _, ok := mm.sidecar[key]; !ok {
		t.Fatalf("sidecar should contain key %q after turn", key)
	}
	// File should exist and be valid JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("sidecar file not written: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("sidecar file empty")
	}
	// Corrupt file should be ignored on load
	_ = os.WriteFile(path, []byte("corrupt json"), 0644)
	loaded := loadSidecar(path)
	if len(loaded) != 0 {
		t.Fatalf("corrupt file should be ignored, got %v", loaded)
	}
}

func TestSidecar_MergeOnReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tui-state.json")
	sid := "sess-reload"
	// Write sidecar with duration for seq 10
	sc := map[string]int64{sidecarKey(sid, 10): 27600} // 27.6s
	_ = saveSidecar(path, sc)
	m := newTestModel()
	m.sidecarPath = path
	m.sidecar = loadSidecar(path)
	m.sessionID = sid
	m.currentModel = "glm-5.3-flash"
	m.SetSize(80, 24)
	// Simulate messagesSince reload with assistant seq 10 and no meta
	fresh := &daemon.GetMessagesResult{
		Messages: []daemon.MessageResult{
			{Seq: 10, Role: "assistant", Content: "reloaded content", Usage: &daemon.UsageResult{TotalTokens: 2776}},
		},
	}
	model, _ := m.Update(messagesSinceMsg{res: fresh, err: nil})
	mm := model.(Model)
	found := false
	for _, e := range mm.Entries() {
		if e.Seq == 10 && e.Role == "assistant" {
			found = true
			if !strings.Contains(e.Meta, "27.6s") && !strings.Contains(e.Meta, "27600ms") {
				t.Fatalf("reloaded entry should contain elapsed from sidecar, got Meta %q", e.Meta)
			}
		}
	}
	if !found {
		t.Fatalf("entry seq 10 not found after reload")
	}
}

// ---------- 6. Clickable rail + footer hotspots hit testing ----------

func TestHitTestRail_PureFunction(t *testing.T) {
	// Rail of height 12 starting at row 1: thirds are context (rel 0-3),
	// plugins (rel 4-7), turnstats (rel 8-11). Row 0 is the title bar.
	if HitTestRail(1, 1, 12) != "context" {
		t.Fatalf("top third should be context")
	}
	if HitTestRail(5, 1, 12) != "plugins" {
		t.Fatalf("middle third should be plugins")
	}
	if HitTestRail(10, 1, 12) != "turnstats" {
		t.Fatalf("bottom third should be turnstats")
	}
	if HitTestRail(0, 1, 12) != "" {
		t.Fatal("title-bar row should be outside the rail")
	}
	if HitTestRail(20, 1, 12) != "" {
		t.Fatal("outside should be empty")
	}
}

func TestHitTestFooter_PureFunction(t *testing.T) {
	w := 80
	footerTop := 15
	footerH := 3
	if HitTestFooter(75, 16, footerTop, footerH, w) != "copy" {
		t.Fatal("rightmost 10 should be copy")
	}
	if HitTestFooter(60, 16, footerTop, footerH, w) != "session" {
		t.Fatal("next 20 should be session")
	}
	if HitTestFooter(40, 16, footerTop, footerH, w) != "model" {
		t.Fatal("next 20 should be model")
	}
	if HitTestFooter(10, 16, footerTop, footerH, w) != "" {
		t.Fatal("left side should be empty")
	}
	if HitTestFooter(70, 10, footerTop, footerH, w) != "" {
		t.Fatal("y outside should be empty")
	}
}

func TestRailClickOpensPanel(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.showSidebar = true
	m.rebuildTranscript()
	transH := m.viewport.Height()
	// Click top third of rail => context; rail starts below the title box.
	// rail X start = 80-32=48, Y=titleHeightRows = first rail row.
	mouse := tea.Mouse{X: 70, Y: titleHeightRows}
	m.handleMouseClick(mouse)
	if m.RailPanel() != "context" {
		t.Fatalf("click top rail should open context panel, got %q", m.RailPanel())
	}
	// Click again should close
	m.handleMouseClick(mouse)
	if m.RailPanel() != "" {
		t.Fatalf("second click should close panel, got %q", m.RailPanel())
	}
	// Click middle => plugins
	mouse2 := tea.Mouse{X: 70, Y: titleHeightRows + transH/2}
	m.handleMouseClick(mouse2)
	if m.RailPanel() != "plugins" {
		t.Fatalf("middle click should open plugins, got %q", m.RailPanel())
	}
	// Keyboard equivalent ctrl+3
	model, _ := m.Update(keyPress("ctrl+3"))
	mm := model.(Model)
	if mm.RailPanel() != "turnstats" {
		t.Fatalf("ctrl+3 should open turnstats, got %q", mm.RailPanel())
	}
	// Esc closes
	model, _ = mm.Update(keyPress("esc"))
	mm = model.(Model)
	if mm.RailPanel() != "" {
		t.Fatal("esc should close rail panel")
	}
}

func TestFooterClickOpensDropdownAndModelPanel(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.sessions = []daemon.SessionResult{{ID: "sess-abc12345"}, {ID: "sess-def67890"}}
	m.sessionID = "sess-abc12345"
	// Setup model list file for model panel
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(cfgPath, []byte(`{"providers":{"p1":{"kind":"openai-compatible","base_url":"http://a/v1","models":["m1","m2"]}}}`), 0644)
	m.configPath = cfgPath
	m.rebuildTranscript()
	footerH := m.measureFooterHeight(80)
	footerTop := titleHeightRows + m.viewport.Height() + 1 + 4
	// Click session hotspot (session zone below the copy zone)
	mouseSession := tea.Mouse{X: 60, Y: footerTop + 1}
	m.handleMouseClick(mouseSession)
	if !m.IsSessionsDropdownVisible() {
		t.Fatalf("click session hotspot should open dropdown, footerTop %d h %d", footerTop, footerH)
	}
	// Click again to close
	m.handleMouseClick(mouseSession)
	if m.IsSessionsDropdownVisible() {
		t.Fatal("second click should close dropdown")
	}
	// Click model hotspot
	mouseModel := tea.Mouse{X: 40, Y: footerTop + 1}
	m.handleMouseClick(mouseModel)
	if !m.IsModelPanelVisible() {
		t.Fatalf("click model hotspot should open model panel")
	}
}

// ---------- 7. Streaming usage persistence (regression) ----------

func TestStreamingTurnPersistsUsage(t *testing.T) {
	// Simulate streaming turn where final assistant message has usage; verify it is persisted and appears in transcript meta and totalTokens
	m := newTestModel()
	m.SetSize(80, 24)
	m.sessionID = "sess-usage"
	m.currentModel = "test-model"
	fc := newFakeClock(time.Now())
	m.SetClock(fc)
	// Execute turn with usage
	res := &daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{
			{Seq: 1, Role: "user", Content: "hi"},
			{Seq: 2, Role: "assistant", Content: "hello", Usage: &daemon.UsageResult{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}},
		},
		Usage: &daemon.UsageResult{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		Model: "test-model",
	}
	model, _ := m.Update(executeTurnMsg{res: res, err: nil})
	mm := model.(Model)
	if mm.TotalTokens() != 30 {
		t.Fatalf("totalTokens should be 30 after streaming turn, got %d", mm.TotalTokens())
	}
	// Verify entry meta contains tokens
	found := false
	for _, e := range mm.Entries() {
		if e.Role == "assistant" && e.Seq == 2 {
			found = true
			if !strings.Contains(e.Meta, "30") && !strings.Contains(e.Meta, "tokens") {
				t.Fatalf("assistant meta should contain tokens, got %q", e.Meta)
			}
		}
	}
	if !found {
		t.Fatal("assistant entry not found")
	}
}

// ---------- 9. Working marker live elapsed ----------

func TestFormatWorkingElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0,0s"},
		{300 * time.Millisecond, "0,3s"},
		{34370 * time.Millisecond, "34,4s"},
		{125 * time.Second, "2m05s"},
		{-time.Second, "0,0s"},
	}
	for _, tc := range cases {
		if got := formatWorkingElapsed(tc.d); got != tc.want {
			t.Fatalf("formatWorkingElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWorkingMarker_ShowsLiveElapsed(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-elapsed"
	m.SetSize(80, 24)
	fc := newFakeClock(time.Now())
	m.SetClock(fc)
	m.input.SetValue("hola")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	// Fresh echo carries the bare marker; elapsed stamps on ticks.
	if !strings.Contains(mm.View().Content, "◌ Working…") {
		t.Fatalf("fresh turn should show Working marker, view missing")
	}
	if strings.Contains(mm.View().Content, "(0,0s)") {
		t.Fatal("no elapsed before the first stamp tick")
	}
	fc.Advance(34400 * time.Millisecond)
	model, _ = mm.Update(spinner.TickMsg{Time: fc.Now(), ID: mm.spinnerModel.ID()})
	mm = model.(Model)
	if !strings.Contains(mm.View().Content, "Working… (34,4s)") {
		t.Fatalf("elapsed should stamp to 34,4s on tick, view missing")
	}
	// Same-second tick: no rebuild (TUI-6 separation holds).
	base := mm.RebuildCount()
	model, _ = mm.Update(spinner.TickMsg{Time: fc.Now(), ID: mm.spinnerModel.ID()})
	mm = model.(Model)
	if mm.RebuildCount() != base {
		t.Fatalf("same-second stamp must not rebuild: before %d after %d", base, mm.RebuildCount())
	}
	if !strings.Contains(mm.View().Content, "Working… (34,4s)") {
		t.Fatal("elapsed must persist without rebuild")
	}
}

// ---------- 8. Retest-4: title bar box, selection default, Working marker ----------

func TestTitleBarBoxedLikeFooter(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.daemonVers = "v9.9.9-test"
	bar := m.renderTitleBar()
	if rows := strings.Count(bar, "\n") + 1; rows != titleHeightRows {
		t.Fatalf("title bar should be exactly %d rows, got %d", titleHeightRows, rows)
	}
	if !strings.Contains(bar, "forge") || !strings.Contains(bar, "v9.9.9-test") {
		t.Fatalf("title bar should name forge and daemon version, got %q", bar)
	}
	for _, r := range []string{"┌", "┐", "└", "┘", "─"} {
		if !strings.Contains(bar, r) {
			t.Fatalf("title bar should be a bordered box like the footer, missing %q", r)
		}
	}
}

func TestWorkingMarker_SendAndClear(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-work"
	m.SetSize(80, 24)
	fc := newFakeClock(time.Now())
	m.SetClock(fc)
	m.input.SetValue("list the files")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	// Echo carries the Working marker while the turn is in flight.
	found := false
	for _, e := range mm.Entries() {
		if e.Role == "user" && e.Local && e.Meta == workingMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent echo should carry Working marker, entries = %+v", mm.Entries())
	}
	if !strings.Contains(mm.View().Content, "Working") {
		t.Fatal("view should show Working marker next to the sent message")
	}
	// Turn completes after 33,5s with 1345 tokens: the user message keeps
	// "33,5s :: 1,345 tokens" instead of the marker.
	fc.Advance(33500 * time.Millisecond)
	res := &daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{
			{Seq: 1, Role: "user", Content: "list the files"},
			{Seq: 2, Role: "assistant", Content: "done"},
		},
		Usage: &daemon.UsageResult{TotalTokens: 1345},
	}
	model, _ = mm.Update(executeTurnMsg{res: res, err: nil})
	mm = model.(Model)
	foundStats := false
	for _, e := range mm.Entries() {
		if e.Meta == workingMarker {
			t.Fatalf("Working marker should be finalized after turn, entries = %+v", mm.Entries())
		}
		if e.Role == "user" && e.Meta == "33,5s :: 1,345 tokens" {
			foundStats = true
		}
		if e.Role == "assistant" && e.Meta == "tokens 1345 · 33,5s" && !e.Summary {
			t.Fatalf("turn summary meta should be flagged Summary, entries = %+v", mm.Entries())
		}
	}
	if !foundStats {
		t.Fatalf("user message should keep turn stats, entries = %+v", mm.Entries())
	}
}

func TestWorkingMarker_ClearedOnError(t *testing.T) {
	m := newTestModel()
	m.entries = []components.Entry{{Role: "user", Content: "hi", Local: true, Meta: workingMarker}}
	m.spinner = true
	m.SetSize(80, 24)
	model, _ := m.Update(executeTurnMsg{res: nil, err: fakeErr("boom")})
	mm := model.(Model)
	for _, e := range mm.Entries() {
		if e.Meta == workingMarker {
			t.Fatal("Working marker should be cleared on turn error")
		}
	}
}

func TestWorkingMarker_ErrorKeepsElapsed(t *testing.T) {
	m := newTestModel()
	m.entries = []components.Entry{{Role: "user", Content: "hi", Local: true, Meta: workingMarker}}
	m.spinner = true
	m.SetSize(80, 24)
	fc := newFakeClock(time.Now())
	m.SetClock(fc)
	m.turnStart = fc.Now()
	fc.Advance(12100 * time.Millisecond)
	model, _ := m.Update(executeTurnMsg{res: nil, err: fakeErr("boom")})
	mm := model.(Model)
	found := false
	for _, e := range mm.Entries() {
		if e.Meta == workingMarker {
			t.Fatal("Working marker should be finalized on turn error")
		}
		if e.Role == "user" && e.Meta == "12,1s" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message should keep elapsed on error, entries = %+v", mm.Entries())
	}
}

func TestWorkingMarker_HaltKeepsElapsed(t *testing.T) {	m := newTestModel()
	m.entries = []components.Entry{{Role: "user", Content: "hi", Local: true, Meta: workingMarker}}
	m.spinner = true
	m.SetSize(80, 24)
	fc := newFakeClock(time.Now())
	m.SetClock(fc)
	m.turnStart = fc.Now()
	fc.Advance(8000 * time.Millisecond)
	model, _ := m.Update(haltResultMsg{})
	mm := model.(Model)
	found := false
	for _, e := range mm.Entries() {
		if e.Meta == workingMarker {
			t.Fatal("Working marker should be finalized on halt")
		}
		if e.Role == "user" && e.Meta == "8,0s" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user message should keep elapsed on halt, entries = %+v", mm.Entries())
	}
}

// ---------- 10. Ghost-turn watchdog ----------
func TestWatchdog_HaltsGhostTurn(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{sinceRes: &daemon.GetMessagesResult{}}
	m.SetClient(fc)
	m.sessionID = "sess-ghost"
	m.SetSize(80, 24)
	fclk := newFakeClock(time.Now())
	m.SetClock(fclk)
	m.spinner = true
	m.turnStart = fclk.Now()
	m.lastDaemonMsg = fclk.Now()
	m.entries = []components.Entry{{Role: "user", Content: "hi"}}
	fclk.Advance(241 * time.Second)
	model, cmd := m.Update(spinner.TickMsg{Time: fclk.Now(), ID: m.spinnerModel.ID()})
	mm := model.(Model)
	if cmd == nil {
		t.Fatal("watchdog should fire ghost protocol after 4m of silence")
	}
	if !strings.Contains(mm.Toast(), "ghost") {
		t.Fatalf("toast should name the ghost turn, got %q", mm.Toast())
	}
	// Execute the protocol like tea does: halt + refetch, then feed back.
	var runCmd func(c tea.Cmd)
	runCmd = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if bm, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range bm {
				runCmd(sub)
			}
			return
		}
		switch msg := msg.(type) {
		case haltResultMsg:
			model, _ = mm.Update(msg)
			mm = model.(Model)
		case messagesSinceMsg:
			model, _ = mm.Update(msg)
			mm = model.(Model)
		case spinner.TickMsg:
			// tick re-arm, ignore
		}
	}
	runCmd(cmd)
	if mm.IsSpinner() {
		t.Fatal("halt should stop the spinner")
	}
}

func TestWatchdog_SuppressedWhileToolRuns(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-ghost"
	m.SetSize(80, 24)
	fclk := newFakeClock(time.Now())
	m.SetClock(fclk)
	m.spinner = true
	m.turnStart = fclk.Now()
	m.lastDaemonMsg = fclk.Now()
	m.pendingTools["call-1"] = pendingTool{name: "shell_exec"}
	fclk.Advance(600 * time.Second)
	model, _ := m.Update(spinner.TickMsg{Time: fclk.Now(), ID: m.spinnerModel.ID()})
	mm := model.(Model)
	if strings.Contains(mm.Toast(), "ghost") {
		t.Fatalf("watchdog must not halt a turn with a running tool, toast %q", mm.Toast())
	}
	if !mm.IsSpinner() {
		t.Fatal("spinner must survive while a tool runs")
	}
}

func TestWatchdog_QuietOnRecentActivity(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-ghost"
	m.SetSize(80, 24)
	fclk := newFakeClock(time.Now())
	m.SetClock(fclk)
	m.spinner = true
	m.turnStart = fclk.Now()
	m.lastDaemonMsg = fclk.Now()
	fclk.Advance(30 * time.Second)
	model, _ := m.Update(spinner.TickMsg{Time: fclk.Now(), ID: m.spinnerModel.ID()})
	mm := model.(Model)
	if strings.Contains(mm.Toast(), "ghost") {
		t.Fatalf("watchdog must stay quiet within the bound, toast %q", mm.Toast())
	}
}

func TestTouchDaemon_OnEvent(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	fclk := newFakeClock(time.Now())
	m.SetClock(fclk)
	m.sessionID = "sess-xyz"
	fclk.Advance(10 * time.Second)
	model, _ := m.Update(deltaNotif("sess-xyz", "chunk"))
	mm := model.(Model)
	if mm.lastDaemonMsg.IsZero() {
		t.Fatal("daemon event should touch the watchdog clock")
	}
}

// ---------- 11. Retest-8: cursor blink, /session panel ----------

func TestInitPrimesCursorBlink(t *testing.T) {
	m := newTestModel()
	// Even with no client (both status cmds nil), Init must return the
	// input cursor blink command: without it the cursor stays solid until
	// the first keystroke.
	if m.Init() == nil {
		t.Fatal("Init must return the cursor blink command")
	}
}

func TestSlashSession_OpensPanelNavigateSelect(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessions = []daemon.SessionResult{{ID: "sess-aaa11111"}, {ID: "sess-bbb22222"}}
	m.sessionID = "sess-aaa11111"
	m.SetSize(80, 24)
	m.input.SetValue("/session")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if !mm.IsSessionsDropdownVisible() {
		t.Fatal("/session should open the sessions panel")
	}
	if mm.SessionsDropdownIdx() != 0 {
		t.Fatalf("panel should start at current session idx 0, got %d", mm.SessionsDropdownIdx())
	}
	// Arrows navigate the visible list.
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	if mm.SessionsDropdownIdx() != 1 {
		t.Fatalf("down should move to idx 1, got %d", mm.SessionsDropdownIdx())
	}
	model, _ = mm.Update(keyPress("up"))
	mm = model.(Model)
	if mm.SessionsDropdownIdx() != 0 {
		t.Fatalf("up should move back to idx 0, got %d", mm.SessionsDropdownIdx())
	}
	// Down from last wraps to first; navigate to idx 1 and select.
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	model, _ = mm.Update(keyPress("enter"))
	mm = model.(Model)
	if mm.SessionID() != "sess-bbb22222" {
		t.Fatalf("enter should switch session, got %q", mm.SessionID())
	}
	if mm.IsSessionsDropdownVisible() {
		t.Fatal("enter should close the panel")
	}
}

// ---------- 12. Copy last response (/copy + footer hotspot) ----------

func TestSlashCopy_CopiesLastResponse(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-copy"
	m.SetSize(80, 24)
	m.entries = []components.Entry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "first"},
		{Role: "assistant", Content: "second and latest"},
	}
	var got string
	old := copyText
	copyText = func(s string) error { got = s; return nil }
	defer func() { copyText = old }()
	m.input.SetValue("/copy")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if got != "second and latest" {
		t.Fatalf("copy should send latest response, got %q", got)
	}
	if !strings.Contains(mm.Toast(), "copied") {
		t.Fatalf("toast should confirm copy, got %q", mm.Toast())
	}
}

func TestSlashCopy_NoResponse(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-copy"
	m.SetSize(80, 24)
	called := false
	old := copyText
	copyText = func(s string) error { called = true; return nil }
	defer func() { copyText = old }()
	m.input.SetValue("/copy")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if called {
		t.Fatal("clipboard must not be touched without a response")
	}
	if !strings.Contains(mm.Toast(), "no response") {
		t.Fatalf("toast should report nothing to copy, got %q", mm.Toast())
	}
}

func TestFooterClickCopy(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.entries = []components.Entry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "copy me"},
	}
	m.rebuildTranscript()
	var got string
	old := copyText
	copyText = func(s string) error { got = s; return nil }
	defer func() { copyText = old }()
	footerTop := titleHeightRows + m.viewport.Height() + 1 + 4
	m.handleMouseClick(tea.Mouse{X: 75, Y: footerTop + 1})
	if got != "copy me" {
		t.Fatalf("footer [copiar] click should copy last response, got %q", got)
	}
}

// ---------- 13. Retest-9: overlays float in-frame ----------

func frameRows(t *testing.T, view string, h int) {
	t.Helper()
	if got := strings.Count(view, "\n") + 1; got != h {
		t.Fatalf("frame must be exactly %d rows, got %d", h, got)
	}
}

func TestOverlay_SessionsDropdownInFrame(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	for i := 0; i < 12; i++ {
		m.sessions = append(m.sessions, daemon.SessionResult{ID: "sess-longlist" + string(rune('a'+i)), MessageCount: i})
	}
	m.sessionID = m.sessions[0].ID
	m.sessionsDropdownVisible = true
	m.sessionFocus = true
	m.sessionsDropdownIdx = 0
	m.sessionFocusIdx = 0
	m.rebuildTranscript()
	m.relayout() // real open paths relayout for the inline hint row
	view := m.View().Content
	frameRows(t, view, 24)
	if !strings.Contains(view, "Sessions") {
		t.Fatal("dropdown title should be visible in-frame")
	}
	// Jump to the last session: the window must follow.
	m.sessionsDropdownIdx = 11
	m.sessionFocusIdx = 11
	view = m.View().Content
	frameRows(t, view, 24)
	if !strings.Contains(view, m.sessions[11].ID[:12]) {
		t.Fatalf("window should follow selection to last session, idx %d", m.sessionsDropdownIdx)
	}
	if !strings.Contains(view, "(12/12)") {
		t.Fatal("windowed list should show position")
	}
}

func TestOverlay_ModelPanelInFrame(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.modelPanelList = []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"}
	m.modelPanelIdx = 7
	m.modelPanelVisible = true
	m.rebuildTranscript()
	m.relayout() // real open paths relayout for the inline hint row
	view := m.View().Content
	frameRows(t, view, 24)
	if !strings.Contains(view, "Select Model") || !strings.Contains(view, "m8") {
		t.Fatal("model panel should be visible in-frame with windowed last model")
	}
}

func TestOverlay_RailPanelInFrame(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.railPanel = "context"
	m.rebuildTranscript()
	view := m.View().Content
	frameRows(t, view, 24)
	if !strings.Contains(view, "Context & tokens") {
		t.Fatal("rail panel should be visible in-frame")
	}
}

func TestOverlay_HelpInFrame(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.openHelp()
	view := m.View().Content
	frameRows(t, view, 24)
	if !strings.Contains(view, "Slash Commands") {
		t.Fatal("help should be visible in-frame")
	}
}
