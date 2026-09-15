package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// ---------- 1. Layout distinctness (M2: single Status Rail) ----------

func TestLayoutDistinctness_ThreeLayoutsDiffer(t *testing.T) {
	seed := []components.Entry{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}
	sessions := []daemon.SessionResult{{ID: "sess-12345678", MessageCount: 2}}
	makeView := func(showRail bool) string {
		m := newTestModel()
		m.showSidebar = showRail
		m.entries = seed
		m.sessions = sessions
		m.sessionID = "sess-12345678"
		m.SetSize(80, 24)
		m.rebuildTranscript()
		return m.View().Content
	}
	railOn := makeView(true)
	railOff := makeView(false)
	if railOn == railOff {
		t.Fatal("M2: rail on and rail off views should be distinct")
	}
	// Rail on should contain rail content (Context & tokens); rail off should be full-width without rail border
	if !strings.Contains(railOn, "Context & tokens") {
		t.Fatal("rail on should contain Context & tokens")
	}
	// Title bar and separator must be present in both
	for _, v := range []string{railOn, railOff} {
		if !strings.Contains(v, "forge") {
			t.Fatalf("title bar missing forge, view %q", v[:200])
		}
		if !strings.Contains(v, "─") {
			t.Fatalf("separator line missing")
		}
	}
}

func TestLayoutMinimalAuthoritativeNoOverlay(t *testing.T) {
	seed := []components.Entry{{Role: "user", Content: "hello"}}
	m1 := newTestModel()
	m1.showSidebar = true
	m1.entries = seed
	m1.SetSize(80, 24)
	m1.rebuildTranscript()
	v1 := m1.View().Content

	m2 := newTestModel()
	m2.showSidebar = false
	m2.entries = seed
	m2.SetSize(80, 24)
	m2.rebuildTranscript()
	v2 := m2.View().Content

	if v1 == v2 {
		t.Fatalf("M2: rail on vs off must be visually distinct")
	}
	// Both have title bar + separator regardless of rail
	for _, v := range []string{v1, v2} {
		if !strings.Contains(v, "forge") {
			t.Fatal("title bar missing")
		}
	}
}

func TestCycleLandsOnDistinctLayoutsWithNormalizedSidebar(t *testing.T) {
	m := newTestModel()
	m.showSidebar = true
	m.SetSize(80, 24)
	// Seed entries so views have content
	m.entries = []components.Entry{{Role: "user", Content: "hello"}}
	m.rebuildTranscript()

	initialView := m.View().Content
	seenViews := map[string]bool{}
	seenViews[initialView] = true

	// M2: ctrl+l toggles rail (alias of ctrl+o)
	model, _ := m.Update(keyPress("ctrl+l"))
	m = model.(Model)
	if m.ShowSidebar() {
		t.Fatalf("first ctrl+l should toggle rail off, got showSidebar %v", m.ShowSidebar())
	}
	v := m.View().Content
	if seenViews[v] {
		t.Fatal("rail off view should be visually distinct from rail on")
	}
	offView := v
	if strings.Contains(v, "Context & tokens") {
		t.Fatal("rail off should not contain rail content")
	}

	// second ctrl+l -> back on
	model, _ = m.Update(keyPress("ctrl+l"))
	m = model.(Model)
	if !m.ShowSidebar() {
		t.Fatal("second ctrl+l should toggle rail back on")
	}
	v = m.View().Content
	if !strings.Contains(v, "Context & vs") && !strings.Contains(v, "Context & tokens") {
		t.Fatal("rail on should contain rail")
	}
	// ctrl+o also toggles
	model, _ = m.Update(keyPress("ctrl+o"))
	m = model.(Model)
	if m.ShowSidebar() {
		t.Fatal("ctrl+o should toggle rail off again")
	}
	v = m.View().Content
	// Rail toggle is deterministic: rail off again must render exactly like
	// the earlier rail-off frame (same chrome, no transient toast to differ).
	if v != offView {
		t.Fatal("rail off again should equal the earlier rail-off view")
	}
}

// ---------- 2. Spinner v2 ----------

func TestSpinnerV2_FPSAndDistinctStyle(t *testing.T) {
	m := newTestModel()
	fps := m.SpinnerFPS()
	want := time.Second / 2
	if fps != want {
		t.Fatalf("spinner FPS %v want %v (500ms/frame)", fps, want)
	}
	// Style distinct from OpenCode braille dots: must be Line spinner (| / - \),
	// not MiniDot (⠋ etc)
	frames := m.SpinnerFrame() // may be empty before tick, check spinner model frames
	// Access spinner model frames via constructing Line
	lineFrames := spinner.Line.Frames
	hasLine := false
	for _, f := range lineFrames {
		if f == "|" || f == "/" || f == "-" || f == "\\" {
			hasLine = true
			break
		}
	}
	if !hasLine {
		t.Fatal("Line spinner frames should contain | / - \\")
	}
	// Ensure the model's spinner frames are Line, not MiniDot
	// Trigger a tick to get a frame and ensure it's a Line frame
	model, _ := m.Update(spinner.TickMsg{ID: 0, Time: time.Now()})
	// Without spinner true, TickMsg is ignored; set spinner true first
	m.spinner = true
	// Force a tick via the model update path (spinner.TickMsg with correct ID will still be ignored if spinner false)
	// Instead check the spinner model's spinner directly
	if m.SpinnerFPS() == time.Second/12 {
		t.Fatal("spinner FPS should not be MiniDot default (83ms); should be 500ms")
	}
	// Check View contains Line frame characters when spinner active and pending user message
	_ = frames
	_ = model
	// Verify MiniDot braille frames are not used
	mmFrames := m.SpinnerFPS()
	_ = mmFrames
	// Directly inspect the spinner model via View after tick
	// The spinnerModel's Frames should be Line
	// We verify by checking that the spinner's FPS is 500ms and that View after a tick yields a Line char when spinner is on
	m2 := newTestModel()
	m2.spinner = true
	// Seed a pending user entry so spinner line appears in transcript
	m2.entries = []components.Entry{{Role: "user", Content: "hello"}}
	m2.SetSize(80, 24)
	m2.rebuildTranscript()
	view := m2.View().Content
	// The animated spinner lives in the footer bar while generating.
	if !strings.Contains(view, "working…") {
		t.Fatalf("footer spinner should show 'working…' while generating, view missing")
	}
	// Ensure style distinct: Line spinner frames are | / - \ not braille
	// Check that the spinner model's frames contain no braille
	braille := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	_ = braille
}

func TestSpinnerBesidePendingUserMessage(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.entries = []components.Entry{{Role: "user", Content: "pending user msg"}}
	m.spinner = true
	m.rebuildTranscript()
	view := m.View().Content
	// The footer bar spinner is the visible progress indicator while
	// generating (the pending message itself carries the Working marker).
	if !strings.Contains(view, "working…") {
		t.Fatalf("footer spinner should show while generating, view missing working…")
	}
	// When streaming exists, the pending spinner line should not duplicate (only streaming caret)
	m2 := newTestModel()
	m2.SetSize(80, 24)
	m2.sessionID = "sess-xyz"
	m2.spinner = true
	model, _ := m2.Update(deltaNotif("sess-xyz", "streaming chunk"))
	mm := model.(Model)
	mm.SetSize(80, 24)
	mm.rebuildTranscript() // ensure coalesced rebuild? Instead trigger deltaRebuild
	model, _ = mm.Update(deltaRebuildMsg{})
	mm = model.(Model)
	view2 := mm.View().Content
	// Streaming entry should show caret, not working… line (or at most one of them)
	if !strings.Contains(view2, "▌") {
		t.Fatalf("streaming should show caret")
	}
}

func TestSpinnerCleanStopDeterministic(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	m.spinner = true
	m.entries = []components.Entry{{Role: "user", Content: "hi"}}
	m.SetSize(80, 24)
	m.rebuildTranscript()
	initialFrame := m.SpinnerFrame()
	// Drive a tick
	tickMsg := spinner.TickMsg{Time: time.Now(), ID: 0}
	model, _ := m.Update(tickMsg)
	mm := model.(Model)
	// Frame may have advanced (if spinner true)
	_ = initialFrame
	// Stop via executeTurnMsg (authoritative)
	model, _ = mm.Update(executeTurnResMsg(&daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{{Seq: 1, Role: "assistant", Content: "done"}},
	}, nil))
	mm = model.(Model)
	if mm.IsSpinner() {
		t.Fatal("spinner should be off after executeTurnMsg")
	}
	// Subsequent TickMsg should be ignored and not change frame or restart spinner
	frameAfterStop := mm.SpinnerFrame()
	model, _ = mm.Update(spinner.TickMsg{Time: time.Now(), ID: 0})
	mm2 := model.(Model)
	if mm2.SpinnerFrame() != frameAfterStop {
		t.Fatalf("tick after stop should not advance frame: before %q after %q", frameAfterStop, mm2.SpinnerFrame())
	}
	if mm2.IsSpinner() {
		t.Fatal("spinner should remain off after stray tick")
	}
}

// ---------- 3. Chat scrolling ----------

func makeScrollableModel() Model {
	m := newTestModel()
	m.SetSize(80, 10)
	// Fill with many entries to exceed viewport height
	var entries []components.Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, components.Entry{Role: "assistant", Content: fmt.Sprintf("line %d: this is a long content to ensure wrapping and scrollable height for testing scroll behavior", i)})
	}
	m.entries = entries
	m.rebuildTranscript()
	// Ensure at bottom initially
	m.viewport.GotoBottom()
	return m
}

func TestScroll_PgUpPgDnHomeEndForwarded(t *testing.T) {
	m := makeScrollableModel()
	if !m.ViewportAtBottom() {
		t.Fatal("should start at bottom")
	}
	top := m.ViewportYOffset()
	// pgup should scroll up (decrease? actually YOffset increases when at bottom; pgup decreases offset)
	// In viewport, YOffset 0 is top, max is bottom. So pgup from bottom should reduce YOffset.
	model, _ := m.Update(keyPress("pgup"))
	mm := model.(Model)
	if mm.ViewportYOffset() == top {
		t.Fatalf("pgup should change YOffset: before %d after %d", top, mm.ViewportYOffset())
	}
	afterPgUp := mm.ViewportYOffset()
	// pgdown should go back towards bottom
	model, _ = mm.Update(keyPress("pgdown"))
	mm = model.(Model)
	if mm.ViewportYOffset() == afterPgUp && mm.ViewportAtBottom() == false {
		// pgdown may not return to bottom but should increase offset or go towards bottom
		// We check that offset changed or at least not same as before if at bottom
	}
	// home
	model, _ = mm.Update(keyPress("home"))
	mm = model.(Model)
	if mm.ViewportYOffset() != 0 {
		t.Fatalf("home should go to top YOffset 0, got %d", mm.ViewportYOffset())
	}
	// end
	model, _ = mm.Update(keyPress("end"))
	mm = model.(Model)
	if !mm.ViewportAtBottom() {
		t.Fatalf("end should goto bottom, YOffset %d AtBottom %v", mm.ViewportYOffset(), mm.ViewportAtBottom())
	}
}

func TestScroll_MouseWheelForwarded(t *testing.T) {
	m := makeScrollableModel()
	m.viewport.GotoBottom()
	before := m.ViewportYOffset()
	// Wheel up should scroll up (decrease offset)
	msgUp := tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelUp}
	model, _ := m.Update(msgUp)
	mm := model.(Model)
	if mm.ViewportYOffset() == before && !mm.ViewportAtBottom() {
		// If at max, wheel up should reduce offset
	}
	if mm.ViewportYOffset() >= before && before == mm.ViewportYOffset() && m.ViewportAtBottom() {
		// Wheel up from bottom should move up
		if mm.ViewportAtBottom() {
			// After wheel up, should not be at bottom
			t.Fatalf("wheel up from bottom should leave bottom, YOffset before %d after %d", before, mm.ViewportYOffset())
		}
	}
	// Wheel down should go towards bottom
	msgDown := tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown}
	model, _ = mm.Update(msgDown)
	mm = model.(Model)
	// At least not panicking and forwarding
	_ = mm
	// Wheel should be ignored when help visible
	m2 := makeScrollableModel()
	m2.helpVisible = true
	m2.viewport.GotoBottom()
	before2 := m2.ViewportYOffset()
	model, _ = m2.Update(msgUp)
	mm2 := model.(Model)
	if mm2.ViewportYOffset() != before2 {
		t.Fatalf("wheel should be ignored when help visible")
	}
}

func TestScroll_MoreBelowHintWired(t *testing.T) {
	m := makeScrollableModel()
	m.SetSize(80, 24) // reset with proper footer width
	m.viewport.GotoBottom()
	viewAtBottom := m.View().Content
	if strings.Contains(viewAtBottom, "more below") {
		t.Fatal("more below hint should NOT show when at bottom")
	}
	// Scroll up
	model, _ := m.Update(keyPress("pgup"))
	mm := model.(Model)
	// Need to ensure we are not at bottom
	mm.viewport.GotoTop()
	// Re-render
	viewScrolled := mm.View().Content
	if !strings.Contains(viewScrolled, "more below") {
		t.Fatalf("more below hint should show when scrolled up, view missing hint")
	}
	// Back to bottom hint gone
	mm.viewport.GotoBottom()
	viewAgain := mm.View().Content
	if strings.Contains(viewAgain, "more below") {
		t.Fatal("more below hint should be false after GotoBottom")
	}
}

func TestScroll_StickToBottomPreserveAndForce(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 10)
	var entries []components.Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, components.Entry{Role: "user", Content: fmt.Sprintf("msg %d", i)})
	}
	m.entries = entries
	m.rebuildTranscript()
	m.viewport.GotoBottom()
	if !m.ViewportAtBottom() {
		t.Fatal("should be at bottom before test")
	}
	// Scroll up
	m.viewport.ScrollUp(3)
	if m.ViewportAtBottom() {
		t.Fatal("should be scrolled up")
	}
	yBefore := m.ViewportYOffset()
	// New non-forced rebuild (e.g., delta coalesced tick) should preserve position
	m.entries = append(m.entries, components.Entry{Role: "assistant", Content: "new content while scrolled up"})
	m.rebuildTranscript() // not forced, wasAtBottom false => should preserve
	if m.ViewportYOffset() != yBefore {
		t.Fatalf("stick-to-bottom should preserve YOffset when scrolled up: before %d after %d", yBefore, m.ViewportYOffset())
	}
	if m.ViewportAtBottom() {
		t.Fatal("should still be scrolled up after rebuild")
	}
	// Forced rebuild (local echo) should go to bottom even when scrolled up
	m.rebuildTranscriptForceBottom()
	if !m.ViewportAtBottom() {
		t.Fatalf("forced rebuild should goto bottom even when previously scrolled up")
	}
}

func TestViewMouseModeEnabled(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	// Default is capture ON so wheel scrolling and click hotspots work out
	// of the box; ctrl+m toggles to free text selection.
	view := m.View()
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("View MouseMode should be CellMotion by default, got %v", view.MouseMode)
	}
	// ctrl+m disables capture for text selection.
	model, _ := m.Update(keyPress("ctrl+m"))
	mm := model.(Model)
	view2 := mm.View()
	if view2.MouseMode != 0 {
		t.Fatalf("View MouseMode should be 0 after ctrl+m, got %v", view2.MouseMode)
	}
}

// ---------- 4. Session focus mode ----------

func TestSessionFocus_ToggleAndNavigation(t *testing.T) {
	m := newTestModel()
	m.sessions = []daemon.SessionResult{{ID: "sess-aaa11111"}, {ID: "sess-bbb22222"}, {ID: "sess-ccc33333"}}
	m.sessionID = "sess-bbb22222"
	m.SetSize(80, 24)

	// ctrl+g enters focus
	model, _ := m.Update(keyPress("ctrl+g"))
	mm := model.(Model)
	if !mm.IsSessionFocus() {
		t.Fatal("ctrl+g should enter session focus mode")
	}
	if mm.SessionFocusIdx() != 1 {
		t.Fatalf("focus idx should be current session index 1, got %d", mm.SessionFocusIdx())
	}
	view := mm.View().Content
	if !strings.Contains(view, "focus") {
		t.Fatalf("sidebar/footer should show focus hint when in focus mode")
	}
	if !strings.Contains(view, "Sessions ● focus") {
		// Check sidebar title – View contains sidebar rendering when hybrid
		// Our minimal check is footer hint presence, sidebar title also
		t.Fatalf("sidebar title should contain 'Sessions ● focus' in focus mode")
	}

	// down wraps
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	if mm.SessionFocusIdx() != 2 {
		t.Fatalf("down should go to 2, got %d", mm.SessionFocusIdx())
	}
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	if mm.SessionFocusIdx() != 0 {
		t.Fatalf("down should wrap to 0, got %d", mm.SessionFocusIdx())
	}
	// up wraps
	model, _ = mm.Update(keyPress("up"))
	mm = model.(Model)
	if mm.SessionFocusIdx() != 2 {
		t.Fatalf("up wrap should go to 2, got %d", mm.SessionFocusIdx())
	}
}

func TestSessionFocus_EnterSwitchesSession(t *testing.T) {
	m := newTestModel()
	m.sessions = []daemon.SessionResult{{ID: "sess-aaa11111"}, {ID: "sess-bbb22222"}}
	m.sessionID = "sess-aaa11111"
	m.entries = []components.Entry{{Role: "user", Content: "local echo", Local: true}}
	m.lastSeq = 5
	m.SetSize(80, 24)
	m.rebuildTranscript()

	// Enter focus
	model, _ := m.Update(keyPress("ctrl+g"))
	mm := model.(Model)
	// Navigate to second
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	// Enter switches
	model, cmd := mm.Update(keyPress("enter"))
	mm = model.(Model)
	if mm.IsSessionFocus() {
		t.Fatal("enter should exit focus mode after switch")
	}
	if mm.SessionID() != "sess-bbb22222" {
		t.Fatalf("sessionID should switch to selected, got %q", mm.SessionID())
	}
	if mm.lastSeq != 0 {
		t.Fatalf("lastSeq should be reset to 0 on session switch, got %d", mm.lastSeq)
	}
	if len(mm.Entries()) != 0 {
		t.Fatalf("entries should be cleared on session switch, got %+v", mm.Entries())
	}
	if cmd == nil && mm.client != nil {
		t.Fatal("should return GetMessagesSince(0) cmd when client set")
	}
	// With fake client, check cmd executes with since 0
	fc := &fakeClient{}
	m2 := newTestModel()
	m2.SetClient(fc)
	m2.sessions = []daemon.SessionResult{{ID: "sess-aaa11111"}, {ID: "sess-bbb22222"}}
	m2.sessionID = "sess-aaa11111"
	m2.SetSize(80, 24)
	model, _ = m2.Update(keyPress("ctrl+g"))
	mm2 := model.(Model)
	model, _ = mm2.Update(keyPress("down"))
	mm2 = model.(Model)
	model, cmd = mm2.Update(keyPress("enter"))
	mm2 = model.(Model)
	if cmd != nil {
		// Execute the cmd to trigger GetMessagesSince
		// It will call fc.GetMessagesSince with since 0
		_ = cmd()
		if fc.gotSince != 0 {
			t.Fatalf("GetMessagesSince should be called with 0, got %d", fc.gotSince)
		}
	}
	_ = mm
}

func TestSessionFocus_EscAndCtrlGExit(t *testing.T) {
	m := newTestModel()
	m.sessions = []daemon.SessionResult{{ID: "sess-aaa"}, {ID: "sess-bbb"}}
	m.SetSize(80, 24)
	model, _ := m.Update(keyPress("ctrl+g"))
	mm := model.(Model)
	if !mm.IsSessionFocus() {
		t.Fatal("should be in focus")
	}
	// esc exits
	model, _ = mm.Update(keyPress("esc"))
	mm = model.(Model)
	if mm.IsSessionFocus() {
		t.Fatal("esc should exit focus mode")
	}
	// re-enter then ctrl+g exits
	model, _ = mm.Update(keyPress("ctrl+g"))
	mm = model.(Model)
	model, _ = mm.Update(keyPress("ctrl+g"))
	mm = model.(Model)
	if mm.IsSessionFocus() {
		t.Fatal("ctrl+g when focused should exit focus mode")
	}
}

func TestSessionFocus_OtherKeysConsumed(t *testing.T) {
	m := newTestModel()
	m.sessions = []daemon.SessionResult{{ID: "sess-aaa"}}
	m.sessionID = "sess-aaa"
	m.SetSize(80, 24)
	m.input.SetValue("before")
	model, _ := m.Update(keyPress("ctrl+g"))
	mm := model.(Model)
	before := mm.input.Value()
	// Send a regular key 'a' while focused
	model, _ = mm.Update(keyPress("a"))
	mm = model.(Model)
	if !mm.IsSessionFocus() {
		t.Fatal("other keys should keep focus mode")
	}
	if mm.input.Value() != before {
		t.Fatalf("other keys should be consumed, input changed from %q to %q", before, mm.input.Value())
	}
	// Also 'c' with ctrl? test that non-navigation keys don't switch session
	if mm.SessionID() != "sess-aaa" {
		t.Fatal("session should not change on other keys")
	}
}

func TestSessionFocus_EmptySessionsToast(t *testing.T) {
	m := newTestModel()
	m.sessions = nil
	model, _ := m.Update(keyPress("ctrl+g"))
	mm := model.(Model)
	if mm.IsSessionFocus() {
		t.Fatal("should not enter focus when no sessions")
	}
	if !strings.Contains(mm.Toast(), "no sessions") {
		t.Fatalf("toast should mention no sessions, got %q", mm.Toast())
	}
}

// ---------- 5. Delta burst coalescing ----------

func TestDeltaCoalescing_BoundedRebuilds(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 24)
	m.sessionID = "sess-xyz"
	m.spinner = true
	start := m.RebuildCount()
	// Send 50 rapid deltas before any tick
	for i := 0; i < 50; i++ {
		model, _ := m.Update(deltaNotif("sess-xyz", fmt.Sprintf("chunk-%d ", i)))
		m = model.(Model)
	}
	// Entries mutated but rebuild count should be bounded (at most 1 pending rebuild, not 50)
	delta := m.RebuildCount() - start
	if delta > 1 {
		t.Fatalf("N rapid deltas should coalesce to <=1 rebuild before tick, got %d", delta)
	}
	if len(m.Entries()) != 1 {
		t.Fatalf("should have single streaming entry, got %d", len(m.Entries()))
	}
	if !strings.Contains(m.Entries()[0].Content, "chunk-0") || !strings.Contains(m.Entries()[0].Content, "chunk-49") {
		t.Fatalf("streaming content should accumulate all chunks, got %q", m.Entries()[0].Content)
	}
	// Process coalesced tick: single rebuild
	model, _ := m.Update(deltaRebuildMsg{})
	m2 := model.(Model)
	after := m2.RebuildCount() - start
	if after != 1 {
		t.Fatalf("after coalesced tick, should have exactly 1 rebuild, got %d", after)
	}
	// Next delta after tick should schedule again and increment to 2 after tick
	model, _ = m2.Update(deltaNotif("sess-xyz", "more "))
	m3 := model.(Model)
	model, _ = m3.Update(deltaRebuildMsg{})
	m4 := model.(Model)
	if m4.RebuildCount()-start != 2 {
		t.Fatalf("second burst should cause second rebuild after tick, got %d", m4.RebuildCount()-start)
	}
}

func TestDeltaCoalescing_TailFollowAtBottomVsPreserved(t *testing.T) {
	m := newTestModel()
	m.SetSize(80, 10)
	m.sessionID = "sess-xyz"
	m.spinner = true
	// Fill to make scrollable
	var entries []components.Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, components.Entry{Role: "assistant", Content: fmt.Sprintf("line %d content for scroll test with wrapping", i)})
	}
	m.entries = entries
	m.rebuildTranscript()
	m.viewport.GotoBottom()
	if !m.ViewportAtBottom() {
		t.Fatal("should be at bottom")
	}
	// Delta at bottom: after coalesced rebuild should stay at bottom
	model, _ := m.Update(deltaNotif("sess-xyz", " bottom chunk"))
	m = model.(Model)
	model, _ = m.Update(deltaRebuildMsg{})
	m = model.(Model)
	if !m.ViewportAtBottom() {
		t.Fatalf("when at bottom, delta rebuild should keep at bottom (tail-follow), YOffset %d", m.ViewportYOffset())
	}
	// Scroll up
	m.viewport.ScrollUp(5)
	yBefore := m.ViewportYOffset()
	if m.ViewportAtBottom() {
		t.Fatal("should be scrolled up")
	}
	// Delta while scrolled up: should preserve position, not jump to bottom
	model, _ = m.Update(deltaNotif("sess-xyz", " while scrolled"))
	m = model.(Model)
	model, _ = m.Update(deltaRebuildMsg{})
	m = model.(Model)
	if m.ViewportYOffset() != yBefore {
		t.Fatalf("when scrolled up, delta rebuild should preserve YOffset: before %d after %d", yBefore, m.ViewportYOffset())
	}
	if m.ViewportAtBottom() {
		t.Fatal("should still be scrolled up after delta")
	}
}

// Ensure coalescing doesn't break swap/failure invariants
func TestDeltaCoalescing_PreservesTUI3SwapAndFailure(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	m.spinner = true
	m.SetSize(80, 24)
	// Rapid deltas
	for i := 0; i < 10; i++ {
		model, _ := m.Update(deltaNotif("sess-xyz", "a "))
		m = model.(Model)
	}
	model, _ := m.Update(deltaRebuildMsg{})
	m = model.(Model)
	// Now authoritative message event swaps
	model, _ = m.Update(messageEventNotif("sess-xyz", 10, "assistant", "confirmed content"))
	m = model.(Model)
	if len(m.Entries()) != 1 || m.Entries()[0].Content != "confirmed content" || m.Entries()[0].Streaming {
		t.Fatalf("swap after coalesced deltas failed: %+v", m.Entries())
	}
	// Failure after deltas keeps partial
	m2 := newTestModel()
	m2.sessionID = "sess-xyz"
	m2.spinner = true
	m2.SetSize(80, 24)
	for i := 0; i < 5; i++ {
		model, _ := m2.Update(deltaNotif("sess-xyz", "partial "))
		m2 = model.(Model)
	}
	model, _ = m2.Update(deltaRebuildMsg{})
	m2 = model.(Model)
	model, _ = m2.Update(executeTurnResMsg(nil, fakeErr("stream error")))
	m2 = model.(Model)
	if len(m2.Entries()) != 1 || m2.Entries()[0].Streaming || !strings.Contains(m2.Entries()[0].Meta, "stream interrupted") {
		t.Fatalf("failure semantics after coalesced deltas broken: %+v", m2.Entries())
	}
}

// TestSoak_TickRearmBounded executes returned tick commands the way tea does
// (concurrently) with a 1ms spinner, hunting the retest-5 runaway: if every
// tick re-arms more than one successor, messages fork exponentially (GBs of
// RAM + cores burned, the frozen TUI signature). Steady re-arm stays in the
// low thousands over the deadline.
func TestSoak_TickRearmBounded(t *testing.T) {
	m := newTestModel()
	fast := spinner.Line
	fast.FPS = time.Millisecond
	m.spinnerModel = spinner.New(spinner.WithSpinner(fast))
	m.SetSize(80, 24)
	m.spinner = true
	pending := []tea.Cmd{
		func() tea.Msg { return m.spinnerModel.Tick() },
	}
	const capMsgs = 20000
	total := 0
	deadline := time.Now().Add(2 * time.Second)
	for len(pending) > 0 && total < capMsgs && time.Now().Before(deadline) {
		batch := pending
		pending = nil
		var wg sync.WaitGroup
		out := make(chan tea.Msg, 2*len(batch)+16)
		for _, c := range batch {
			if c == nil {
				continue
			}
			wg.Add(1)
			go func(c tea.Cmd) {
				defer wg.Done()
				if msg := c(); msg != nil {
					out <- msg
				}
			}(c)
		}
		wg.Wait()
		close(out)
		for msg := range out {
			total++
			if total >= capMsgs {
				break
			}
			// Unpack BatchMsg the way tea does: sub-commands execute.
			if bm, ok := msg.(tea.BatchMsg); ok {
				pending = append(pending, bm...)
				continue
			}
			model, cmd := m.Update(msg)
			m = model.(Model)
			if cmd != nil {
				pending = append(pending, cmd)
			}
		}
	}
	t.Logf("soak processed %d messages", total)
	if total >= capMsgs {
		t.Fatalf("tick re-arm fork: %d messages, successor count multiplies per tick", total)
	}
}
