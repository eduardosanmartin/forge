package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// fakeSave captures persistence calls.
type fakeSave struct {
	calls []TUIConfig
	err   error
}

func (f *fakeSave) fn(cfg TUIConfig) error {
	f.calls = append(f.calls, cfg)
	return f.err
}

// helpers to make KeyPressMsg
func keyPress(s string) tea.KeyPressMsg {
	// Parse via tea.Key string matching: we can construct via helper using tea.Key
	// But for tests we can simulate by constructing a Key with Text and using String() override.
	// Simpler: create a KeyPressMsg where String() returns s via type alias trick.
	// We use the ultraviolet key parsing: create a tea.Key and set fields so String() yields s.
	// Instead, we directly craft a KeyPressMsg whose underlying Key.String() returns s
	// by setting Code/Text appropriately. For ctrl combos, we simulate via Mod.
	// Easiest: use tea.KeyPressMsg(tea.Key{Text: s})? That won't yield "ctrl+o".
	// We workaround: we use key.Matches which compares msg.String() to binding keys.
	// So we can fake a type that implements String() as s. Create a struct with String method.
	// However Update expects tea.KeyPressMsg specifically (type alias). We can instead
	// create a tea.Key with appropriate Mod/Code such that String() equals s.
	// For simplicity, we directly create a tea.KeyPressMsg via casting a tea.Key that we
	// manually set to produce desired String(). We test by using the real tea.Key construction.
	//
	// To avoid complex construction, we use a helper that builds a tea.KeyPressMsg
	// whose String() is overridden via embedding? Actually tea.KeyPressMsg is alias to tea.Key,
	// so its String() is tea.Key.String() which delegates to uv.Key.String().
	// We can just set Text to s and Code to 0 and it will return s.
	// key.Matches compares k.String() to binding keys, so if we set Text="ctrl+o", it will match "ctrl+o".
	// This is a test-only shortcut — the binding key is "ctrl+o", and msg.String()="ctrl+o" will match.
	return tea.KeyPressMsg(tea.Key{Text: s})
}

func newTestModel() Model {
	cfg := DefaultTUIConfig()
	pal := MustGetPalette("ember")
	m := NewModel(cfg, pal, "ember", ".forge/config.json", nil)
	m.SetSize(80, 24)
	return m
}

func TestLayoutCycleOrderAndPersistence(t *testing.T) {
	m := newTestModel()
	fs := &fakeSave{}
	m.SetSaveFn(fs.fn)

	// M2: ctrl+l is alias of ctrl+o (rail toggle), not 3-layout cycle. Verify toggle and persistence.
	initial := m.ShowSidebar()
	model, _ := m.Update(keyPress("ctrl+l"))
	mm := model.(Model)
	if mm.ShowSidebar() == initial {
		t.Fatalf("ctrl+l should toggle rail, initial %v after %v", initial, mm.ShowSidebar())
	}
	if len(fs.calls) == 0 {
		t.Fatal("persistence hook not called on rail toggle via ctrl+l")
	}
	// Second toggle via ctrl+l returns
	model, _ = mm.Update(keyPress("ctrl+l"))
	mm2 := model.(Model)
	if mm2.ShowSidebar() != initial {
		t.Fatalf("second ctrl+l should toggle back, got %v want %v", mm2.ShowSidebar(), initial)
	}
	// ctrl+o also toggles (alias behavior)
	model, _ = mm2.Update(keyPress("ctrl+o"))
	mm3 := model.(Model)
	if mm3.ShowSidebar() == initial {
		t.Fatalf("ctrl+o should toggle rail")
	}
}

func TestSidebarTogglePerLayout(t *testing.T) {
	// hybrid toggles overlay
	m := newTestModel()
	if !m.ShowSidebar() {
		t.Fatal("default sidebar true")
	}
	// ctrl+o in hybrid should toggle off
	model, _ := m.Update(keyPress("ctrl+o"))
	mm := model.(Model)
	if mm.ShowSidebar() {
		t.Fatal("hybrid toggle should hide sidebar")
	}
	// toggle back
	model, _ = mm.Update(keyPress("ctrl+o"))
	mm = model.(Model)
	if !mm.ShowSidebar() {
		t.Fatal("hybrid toggle should show again")
	}

	// session layout
	m = newTestModel()
	m.layout = LayoutSession
	m.showSidebar = true
	model, _ = m.Update(keyPress("ctrl+o"))
	mm = model.(Model)
	if mm.ShowSidebar() {
		t.Fatal("session toggle should hide column")
	}

	// minimal layout: overlay toggle
	m = newTestModel()
	m.layout = LayoutMinimal
	m.showSidebar = false
	model, _ = m.Update(keyPress("ctrl+o"))
	mm = model.(Model)
	if !mm.ShowSidebar() {
		t.Fatal("minimal toggle should show overlay")
	}
}

func TestSlashCommandParsingValidInvalid(t *testing.T) {
	m := newTestModel()
	fs := &fakeSave{}
	m.SetSaveFn(fs.fn)

	cases := []struct {
		name       string
		input      string
		wantLayout string
		wantPal    string
		wantToast  string
		wantHelp   bool
		shouldPersist bool
	}{
		{"layout toggles rail", "/layout minimal", "", "", "rail", false, true},
		{"layout args ignored", "/layout bad", "", "", "rail", false, false},
		{"valid palette", "/palette ember", "", "ember", "palette", false, true},
		{"invalid palette", "/palette unknown", "", "", "unknown palette", false, false},
		{"help", "/help", "", "", "", true, false},
		{"unknown", "/unknown", "", "", "unknown command", false, false},
		{"bare layout toggles rail", "/layout", "", "", "rail", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Set input value via textarea
			m.input.SetValue(tc.input)
			// Send enter
			model, _ := m.Update(keyPress("enter"))
			mm := model.(Model)
			if tc.wantLayout != "" && mm.Layout() != tc.wantLayout {
				t.Fatalf("layout got %q want %q", mm.Layout(), tc.wantLayout)
			}
			if tc.wantPal != "" && mm.PaletteName() != tc.wantPal {
				t.Fatalf("palette got %q want %q", mm.PaletteName(), tc.wantPal)
			}
			if tc.wantHelp {
				if !mm.IsHelpVisible() {
					t.Fatal("help should be visible after /help")
				}
			} else if tc.wantToast != "" && !strings.Contains(strings.ToLower(mm.Toast()), strings.ToLower(tc.wantToast)) {
				t.Fatalf("toast %q should contain %q", mm.Toast(), tc.wantToast)
			}
			// input should be cleared on slash handling
			if mm.input.Value() != "" {
				t.Fatalf("input not cleared after slash")
			}
		})
	}
}

func TestKeyHandlingPrecedenceGlobalBeforeTextarea(t *testing.T) {
	m := newTestModel()
	// Put some text in input
	m.input.SetValue("hello")
	// ctrl+o should toggle sidebar, not insert text
	model, _ := m.Update(keyPress("ctrl+o"))
	mm := model.(Model)
	if mm.input.Value() != "hello" {
		t.Fatalf("global key should not modify textarea value, got %q", mm.input.Value())
	}
	if mm.ShowSidebar() {
		t.Fatal("ctrl+o should toggle sidebar even when textarea focused")
	}
	// ctrl+l should toggle rail (alias of ctrl+o), not insert
	m = newTestModel()
	initial := m.ShowSidebar()
	m.input.SetValue("test")
	model, _ = m.Update(keyPress("ctrl+l"))
	mm = model.(Model)
	if mm.ShowSidebar() == initial {
		t.Fatalf("ctrl+l should toggle rail even with textarea focus, initial %v after %v", initial, mm.ShowSidebar())
	}
	if mm.input.Value() != "test" {
		t.Fatalf("ctrl+l should not modify textarea")
	}
}

func TestQuitKey(t *testing.T) {
	m := newTestModel()
	_, cmd := m.Update(keyPress("ctrl+c"))
	if cmd == nil {
		t.Fatal("quit should return tea.Quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected QuitMsg got %T", msg)
	}
}

func TestInputShiftEnterNewlineVsEnterSubmit(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("hello")
	// shift+enter should insert newline (delegate to textarea)
	model, _ := m.Update(keyPress("shift+enter"))
	mm := model.(Model)
	val := mm.input.Value()
	if !strings.Contains(val, "\n") {
		t.Fatalf("shift+enter should insert newline, got %q", val)
	}
	if len(mm.Entries()) != 0 {
		t.Fatal("shift+enter should not submit")
	}

	// enter should submit and clear
	m = newTestModel()
	m.input.SetValue("hello world")
	model, _ = m.Update(keyPress("enter"))
	mm = model.(Model)
	if len(mm.Entries()) != 1 || mm.Entries()[0].Content != "hello world" {
		t.Fatalf("enter should submit, entries %+v", mm.Entries())
	}
	if mm.input.Value() != "" {
		t.Fatalf("enter should clear input, got %q", mm.input.Value())
	}
}

func TestTextareaDeleteBackwardRebind(t *testing.T) {
	m := newTestModel()
	keys := m.input.KeyMap().DeleteCharacterBackward.Keys()
	if len(keys) != 1 || keys[0] != "backspace" {
		t.Fatalf("DeleteCharacterBackward should be backspace only, got %v", keys)
	}
	// Ensure ctrl+h is not bound
	for _, k := range keys {
		if k == "ctrl+h" {
			t.Fatal("ctrl+h should be free")
		}
	}
}

func TestConfigPersistenceFailureShowsToast(t *testing.T) {
	m := newTestModel()
	fs := &fakeSave{err: fakeErr("disk full")}
	m.SetSaveFn(fs.fn)
	m.input.SetValue("/layout minimal")
	model, _ := m.Update(keyPress("enter"))
	mm := model.(Model)
	if !strings.Contains(mm.Toast(), "disk full") {
		t.Fatalf("toast should show save error, got %q", mm.Toast())
	}
}

func TestViewRoutingPerLayout(t *testing.T) {
	seed := []components.Entry{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world", Meta: "tokens 10"},
	}
	for _, layout := range []string{LayoutHybrid, LayoutSession, LayoutMinimal} {
		t.Run(layout, func(t *testing.T) {
			m := newTestModel()
			m.layout = layout
			m.showSidebar = true
			m.entries = seed
			m.sessions = []daemon.SessionResult{{ID: "sess-1234567890", MessageCount: 2}}
			m.sessionID = "sess-1234567890"
			m.SetSize(80, 24)
			m.rebuildTranscript()
			view := m.View()
			content := view.Content
			if content == "" {
				t.Fatal("View empty")
			}
			// All layouts should contain transcript content
			if !strings.Contains(content, "hello") || !strings.Contains(content, "world") {
				t.Fatalf("view missing transcript content for %s", layout)
			}
		})
	}
}

func TestDaemonErrorInFooter(t *testing.T) {
	m := newTestModel()
	m.daemonErr = "daemon unreachable: dial failed"
	m.SetSize(80, 24)
	view := m.View().Content
	// Search the whole frame: the boxed title bar occupies the first rows,
	// so a fixed prefix window would miss the footer.
	if !strings.Contains(view, "daemon unreachable") {
		t.Fatalf("footer should show daemon error, view %q", view)
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// fakeClient implements TUIClient for daemon-flow tests.
type fakeClient struct {
	turnRes       *daemon.ExecuteTurnResult
	turnErr       error
	sinceRes      *daemon.GetMessagesResult
	gotSince      int
	turnMsg       string
	haltCalled    bool
	haltReason    string
	haltErr       error
	resumeCalled  bool
	resumeErr     error
	switchModel   string
	switchErr     error
	markCalled    bool
	markErr       error
	eventsCh      chan daemon.JSONRPCNotification
	eventsErr     error
	pluginRes     *daemon.PluginListResult
	pluginErr     error
	skillRes      *daemon.SkillListResult
	skillErr      error
	pluginCalled  bool
	skillCalled   bool
}

func (f *fakeClient) Status() (*daemon.StatusResult, error) {
	return &daemon.StatusResult{Running: true, Addr: "127.0.0.1:1", Version: "test"}, nil
}

func (f *fakeClient) ListSessions(limit int) (*daemon.ListSessionsResult, error) {
	return &daemon.ListSessionsResult{}, nil
}

func (f *fakeClient) CreateSession() (*daemon.SessionResult, error) {
	return &daemon.SessionResult{ID: "sess-fake"}, nil
}

func (f *fakeClient) ExecuteTurn(sessionID, message string) (*daemon.ExecuteTurnResult, error) {
	f.turnMsg = message
	return f.turnRes, f.turnErr
}

func (f *fakeClient) GetMessagesSince(sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error) {
	f.gotSince = sinceSeq
	return f.sinceRes, nil
}

func (f *fakeClient) HaltSession(sessionID, reason string) error {
	f.haltCalled = true
	f.haltReason = reason
	return f.haltErr
}
func (f *fakeClient) ResumeSession(sessionID string) error {
	f.resumeCalled = true
	return f.resumeErr
}
func (f *fakeClient) SwitchModel(sessionID, model string) error {
	f.switchModel = model
	return f.switchErr
}
func (f *fakeClient) MarkSuccess(sessionID string) error {
	f.markCalled = true
	return f.markErr
}
func (f *fakeClient) PluginList() (*daemon.PluginListResult, error) {
	f.pluginCalled = true
	if f.pluginRes != nil || f.pluginErr != nil {
		return f.pluginRes, f.pluginErr
	}
	return &daemon.PluginListResult{Plugins: []daemon.PluginInfoResult{}}, nil
}
func (f *fakeClient) SkillList() (*daemon.SkillListResult, error) {
	f.skillCalled = true
	if f.skillRes != nil || f.skillErr != nil {
		return f.skillRes, f.skillErr
	}
	return &daemon.SkillListResult{Skills: []daemon.SkillInfoResult{}}, nil
}
func (f *fakeClient) Events(ctx context.Context) (<-chan daemon.JSONRPCNotification, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	if f.eventsCh != nil {
		return f.eventsCh, nil
	}
	ch := make(chan daemon.JSONRPCNotification)
	return ch, nil
}

func msgResult(seq int, role, content string) daemon.MessageResult {
	return daemon.MessageResult{Seq: seq, Role: role, Content: content}
}

// Regression: execute turn must append turn messages exactly once (no
// get_messages_since(0) refetch duplicating the whole history) and advance
// lastSeq.
func TestExecuteTurnAppendsOnceAndAdvancesSeq(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{
		turnRes: &daemon.ExecuteTurnResult{
			Messages: []daemon.MessageResult{
				msgResult(1, "user", "hi"),
				msgResult(2, "assistant", "hello"),
			},
		},
	}
	m.SetClient(fc)
	m.sessionID = "sess-fake"

	// Simulate existing rendered history up to seq 5 (e.g. resumed session).
	m.lastSeq = 5
	m.entries = []components.Entry{{Role: "user", Content: "old", Seq: 4}, {Role: "assistant", Content: "older", Seq: 5}}

	model, cmd := m.Update(executeTurnResMsg(fc.turnRes, nil))
	mm := model.(Model)
	if cmd != nil {
		t.Fatal("execute turn must not trigger a refetch (duplicate guard)")
	}
	got := mm.Entries()
	// 2 prior + user+assistant of the turn = 4; the turn user message seq(1)
	// is lower than lastSeq(5) but it belongs to the authoritative turn
	// payload, so it is still rendered once.
	if len(got) != 4 {
		t.Fatalf("entries after turn = %d, want 4 (no duplicates): %+v", len(got), got)
	}
	if mm.lastSeq != 5 {
		t.Fatalf("lastSeq = %d, want 5 (turn seqs below history must not lower it)", mm.lastSeq)
	}
}

// Regression: the optimistic local echo must be replaced by the
// daemon-confirmed copy, never duplicated.
func TestExecuteTurnReplacesLocalEcho(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{
		turnRes: &daemon.ExecuteTurnResult{
			Messages: []daemon.MessageResult{
				msgResult(1, "user", "hello world"),
				msgResult(2, "assistant", "hi there"),
			},
		},
	}
	m.SetClient(fc)
	m.sessionID = "sess-fake"

	// Send via enter: creates the local echo.
	m.input.SetValue("hello world")
	model, _ := m.Update(keyPress("enter"))
	m = model.(Model)
	if len(m.Entries()) != 1 || !m.Entries()[0].Local {
		t.Fatalf("expected single local echo entry, got %+v", m.Entries())
	}

	model, _ = m.Update(executeTurnResMsg(fc.turnRes, nil))
	mm := model.(Model)
	got := mm.Entries()
	if len(got) != 2 {
		t.Fatalf("echo not replaced: entries = %+v", got)
	}
	if got[0].Local {
		t.Fatal("first entry should be the daemon-confirmed copy, not the local echo")
	}
	if got[0].Content != "hello world" || got[0].Seq != 1 {
		t.Fatalf("first entry = %+v, want daemon user msg seq 1", got[0])
	}
	if mm.lastSeq != 2 {
		t.Fatalf("lastSeq = %d, want 2", mm.lastSeq)
	}
}

// Regression: messagesSince must append only messages newer than lastSeq.
func TestMessagesSinceSkipsStale(t *testing.T) {
	m := newTestModel()
	m.lastSeq = 2
	m.entries = []components.Entry{{Role: "user", Content: "a", Seq: 1}, {Role: "assistant", Content: "b", Seq: 2}}

	stale := &daemon.GetMessagesResult{Messages: []daemon.MessageResult{
		msgResult(1, "user", "a"),
		msgResult(2, "assistant", "b"),
		msgResult(3, "user", "fresh"),
	}}
	model, _ := m.Update(messagesSinceResMsg(stale, nil))
	mm := model.(Model)
	got := mm.Entries()
	if len(got) != 3 {
		t.Fatalf("stale messages re-appended: entries = %+v", got)
	}
	if got[2].Content != "fresh" || got[2].Seq != 3 {
		t.Fatalf("third entry = %+v, want fresh seq 3", got[2])
	}
	if mm.lastSeq != 3 {
		t.Fatalf("lastSeq = %d, want 3", mm.lastSeq)
	}
}

// helpers to build daemon result msgs directly.
func executeTurnResMsg(res *daemon.ExecuteTurnResult, err error) tea.Msg {
	return executeTurnMsg{res: res, err: err}
}

func messagesSinceResMsg(res *daemon.GetMessagesResult, err error) tea.Msg {
	return messagesSinceMsg{res: res, err: err}
}
