package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
)

// Helpers to build daemon notifications for tests.
func notifPayload(method string, payload any) daemon.JSONRPCNotification {
	data, _ := json.Marshal(payload)
	return daemon.JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  data,
	}
}

func messageEventNotif(sessionID string, seq int, role, content string) daemonEventMsg {
	p := daemon.MessageEventPayload{
		SessionID: sessionID,
	}
	p.Message.Seq = seq
	p.Message.Role = role
	p.Message.Content = content
	p.Message.ID = int64(seq)
	p.Message.CreatedAt = time.Now().Unix()
	return daemonEventMsg{notif: notifPayload(daemon.MethodMessageEvent, p)}
}

func toolCallStartedNotif(sessionID, toolCallID, name string) daemonEventMsg {
	p := daemon.ToolCallEventPayload{
		SessionID:  sessionID,
		ToolCallID: toolCallID,
		Name:       name,
		Status:     "started",
	}
	return daemonEventMsg{notif: notifPayload(daemon.MethodToolCallEvent, p)}
}

func toolCallFinishedNotif(sessionID, toolCallID, name, status string) daemonEventMsg {
	p := daemon.ToolCallEventPayload{
		SessionID:  sessionID,
		ToolCallID: toolCallID,
		Name:       name,
		Status:     status,
	}
	return daemonEventMsg{notif: notifPayload(daemon.MethodToolCallEvent, p)}
}

func TestMessageEventRespectsLastSeq(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	m.lastSeq = 2
	m.entries = []components.Entry{{Role: "user", Content: "a", Seq: 1}, {Role: "assistant", Content: "b", Seq: 2}}

	// stale seq 1 should be ignored
	model, _ := m.Update(messageEventNotif("sess-xyz", 1, "user", "old"))
	mm := model.(Model)
	if len(mm.Entries()) != 2 {
		t.Fatalf("stale event should be ignored, got %d entries", len(mm.Entries()))
	}
	// fresh seq 3 should append
	model, _ = mm.Update(messageEventNotif("sess-xyz", 3, "assistant", "fresh"))
	mm = model.(Model)
	if len(mm.Entries()) != 3 {
		t.Fatalf("fresh event not appended, got %d", len(mm.Entries()))
	}
	if mm.Entries()[2].Content != "fresh" || mm.Entries()[2].Seq != 3 {
		t.Fatalf("wrong entry %+v", mm.Entries()[2])
	}
	if mm.lastSeq != 3 {
		t.Fatalf("lastSeq %d want 3", mm.lastSeq)
	}
}

func TestToolCallEventDurationWithFakeClock(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	fc := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m.SetClock(fc)

	// started
	model, _ := m.Update(toolCallStartedNotif("sess-xyz", "call-1", "fs_read"))
	mm := model.(Model)
	if len(mm.Entries()) != 1 {
		t.Fatalf("started should create entry, got %d", len(mm.Entries()))
	}
	if mm.Entries()[0].ToolName != "fs_read" || mm.Entries()[0].IsTool != true {
		t.Fatalf("wrong tool entry %+v", mm.Entries()[0])
	}
	if mm.Entries()[0].Meta != "" {
		t.Fatalf("pending should have empty meta, got %q", mm.Entries()[0].Meta)
	}

	// advance 123ms and finish
	fc.Advance(123 * time.Millisecond)
	model, _ = mm.Update(toolCallFinishedNotif("sess-xyz", "call-1", "fs_read", "finished"))
	mm = model.(Model)
	if len(mm.Entries()) != 1 {
		t.Fatalf("finished should not add entry, got %d", len(mm.Entries()))
	}
	if mm.Entries()[0].Meta != "123ms" {
		t.Fatalf("duration meta = %q want 123ms", mm.Entries()[0].Meta)
	}
	// Verify rendering contains expected line
	pal := MustGetPalette("ember")
	content := components.BuildContent(mm.Entries(), components.Palette{BG: pal.BG, BGElevated: pal.BGElevated, Border: pal.Border, Text: pal.Text, Dim: pal.Dim, Faint: pal.Faint, Accent: pal.Accent, Success: pal.Success, Warning: pal.Warning, Error: pal.Error}, 80)
	if !strings.Contains(content, "fs_read") || !strings.Contains(content, "123ms") {
		t.Fatalf("render missing tool duration, got %q", content)
	}
	if !strings.Contains(content, "⏺") {
		t.Fatalf("render missing tool marker")
	}
}

func TestToolCallOrphanCompletionIgnored(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	fc := newFakeClock(time.Now())
	m.SetClock(fc)

	// No pending, directly finish -> should not panic and no entry added
	model, _ := m.Update(toolCallFinishedNotif("sess-xyz", "orphan-id", "fs_read", "finished"))
	mm := model.(Model)
	if len(mm.Entries()) != 0 {
		t.Fatalf("orphan completion should be ignored, got %d entries", len(mm.Entries()))
	}
	// Also test error status orphan
	model, _ = mm.Update(toolCallFinishedNotif("sess-xyz", "orphan2", "fs_read", "error"))
	mm = model.(Model)
	if len(mm.Entries()) != 0 {
		t.Fatalf("orphan error should be ignored")
	}
}

func TestToolCallHeuristicNameFallback(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	fc := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m.SetClock(fc)

	// started with empty toolCallID (heuristic case)
	emptyIDStart := daemon.ToolCallEventPayload{
		SessionID: "sess-xyz",
		Name:      "my_tool",
		Status:    "started",
	}
	model, _ := m.Update(daemonEventMsg{notif: notifPayload(daemon.MethodToolCallEvent, emptyIDStart)})
	mm := model.(Model)
	if len(mm.Entries()) != 1 {
		t.Fatalf("heuristic started not added")
	}
	fc.Advance(50 * time.Millisecond)
	emptyIDFinish := daemon.ToolCallEventPayload{
		SessionID: "sess-xyz",
		Name:      "my_tool",
		Status:    "finished",
	}
	model, _ = mm.Update(daemonEventMsg{notif: notifPayload(daemon.MethodToolCallEvent, emptyIDFinish)})
	mm = model.(Model)
	if mm.Entries()[0].Meta != "50ms" {
		t.Fatalf("heuristic finish duration %q want 50ms", mm.Entries()[0].Meta)
	}
}

func TestLiveEventPlusExecuteTurnDedup(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	// Live event seq 3 arrives first
	model, _ := m.Update(messageEventNotif("sess-xyz", 3, "assistant", "live content"))
	mm := model.(Model)
	if len(mm.Entries()) != 1 || mm.lastSeq != 3 {
		t.Fatalf("live event failed %+v lastSeq %d", mm.Entries(), mm.lastSeq)
	}
	// ExecuteTurn result contains same seq 3 (duplicate) plus seq 4
	turnRes := &daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{
			{Seq: 3, Role: "assistant", Content: "live content"},
			{Seq: 4, Role: "assistant", Content: "new turn content"},
		},
	}
	model, _ = mm.Update(executeTurnResMsg(turnRes, nil))
	mm = model.(Model)
	// Should dedup seq 3, only seq 4 appended => total 2 entries
	if len(mm.Entries()) != 2 {
		t.Fatalf("dedup failed, entries %+v", mm.Entries())
	}
	if mm.Entries()[1].Content != "new turn content" || mm.Entries()[1].Seq != 4 {
		t.Fatalf("second entry wrong %+v", mm.Entries()[1])
	}
	if mm.lastSeq != 4 {
		t.Fatalf("lastSeq %d want 4", mm.lastSeq)
	}
}

func TestHaltCtrlHStopsSpinnerAndCallsRPC(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	m.spinner = true
	fc := &fakeClient{}
	m.SetClient(fc)

	model, cmd := m.Update(keyPress("ctrl+h"))
	mm := model.(Model)
	if mm.spinner {
		t.Fatal("spinner should be off after halt key")
	}
	if cmd == nil {
		t.Fatal("halt should return cmd")
	}
	msg := cmd()
	hr, ok := msg.(haltResultMsg)
	if !ok {
		t.Fatalf("expected haltResultMsg got %T", msg)
	}
	if hr.err != nil {
		t.Fatalf("halt err %v", hr.err)
	}
	if !fc.haltCalled || fc.haltReason != "user halt via TUI" {
		t.Fatalf("halt not called with correct reason, got %q called %v", fc.haltReason, fc.haltCalled)
	}
	// Apply result message to get toast
	model, _ = mm.Update(hr)
	mm = model.(Model)
	if mm.Toast() != "halted" {
		t.Fatalf("toast %q want halted", mm.Toast())
	}
	if mm.IsSpinner() {
		t.Fatal("spinner should remain off after halt result")
	}
}

func TestHaltCtrlHNoSessionToast(t *testing.T) {
	m := newTestModel()
	m.sessionID = ""
	m.spinner = true
	model, cmd := m.Update(keyPress("ctrl+h"))
	mm := model.(Model)
	if cmd != nil {
		t.Fatalf("no session should not return halt cmd")
	}
	if !strings.Contains(mm.Toast(), "no session") {
		t.Fatalf("toast %q should mention no session", mm.Toast())
	}
}

func TestSlashResumeModelMarkToasts(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		fakeSetup func(*fakeClient)
		wantToast string
		check     func(*fakeClient) bool
	}{
		{
			name:      "resume success",
			input:     "/resume",
			wantToast: "resumed",
			check:     func(f *fakeClient) bool { return f.resumeCalled },
		},
		{
			name:      "resume error",
			input:     "/resume",
			fakeSetup: func(f *fakeClient) { f.resumeErr = fakeErr("resume failed") },
			wantToast: "resume failed",
		},
		{
			name:      "model valid",
			input:     "/model gpt-4",
			wantToast: "model → gpt-4",
			check:     func(f *fakeClient) bool { return f.switchModel == "gpt-4" },
		},
		{
			name:      "model invalid empty",
			input:     "/model",
			wantToast: "usage:",
		},
		{
			name:      "model error",
			input:     "/model bad-model",
			fakeSetup: func(f *fakeClient) { f.switchErr = fakeErr("model unavailable") },
			wantToast: "model unavailable",
		},
		{
			name:      "mark success",
			input:     "/mark",
			wantToast: "marked success",
			check:     func(f *fakeClient) bool { return f.markCalled },
		},
		{
			name:      "mark error",
			input:     "/mark",
			fakeSetup: func(f *fakeClient) { f.markErr = fakeErr("mark failed") },
			wantToast: "mark failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.sessionID = "sess-xyz"
			fc := &fakeClient{}
			if tc.fakeSetup != nil {
				tc.fakeSetup(fc)
			}
			m.SetClient(fc)
			m.input.SetValue(tc.input)
			model, cmd := m.Update(keyPress("enter"))
			mm := model.(Model)
			if cmd == nil {
				// toast already set for validation errors
				if !strings.Contains(strings.ToLower(mm.Toast()), strings.ToLower(tc.wantToast)) {
					t.Fatalf("toast %q want %q", mm.Toast(), tc.wantToast)
				}
				return
			}
			// Execute the RPC cmd to get result msg
			msg := cmd()
			switch v := msg.(type) {
			case resumeResultMsg:
				model, _ = mm.Update(v)
			case switchModelResultMsg:
				model, _ = mm.Update(v)
			case markSuccessResultMsg:
				model, _ = mm.Update(v)
			default:
				t.Fatalf("unexpected msg type %T", v)
			}
			mm = model.(Model)
			if !strings.Contains(strings.ToLower(mm.Toast()), strings.ToLower(tc.wantToast)) {
				t.Fatalf("toast %q want %q", mm.Toast(), tc.wantToast)
			}
			if tc.check != nil && !tc.check(fc) {
				t.Fatalf("check failed for %s", tc.name)
			}
			// /mark should flag sidebar
			if tc.input == "/mark" && tc.fakeSetup == nil {
				if !mm.MarkedIDs()["sess-xyz"] {
					t.Fatal("mark should set markedIDs")
				}
			}
		})
	}
}

func TestFooterTokenAccumulationAndFormatting(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	m.SetSize(80, 24)

	// First turn with 4000 tokens
	turn1 := &daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{{Seq: 1, Role: "assistant", Content: "hi", Usage: &daemon.UsageResult{TotalTokens: 4000}}},
		Usage:    &daemon.UsageResult{TotalTokens: 4000},
	}
	model, _ := m.Update(executeTurnResMsg(turn1, nil))
	mm := model.(Model)
	if mm.TotalTokens() != 4000 {
		t.Fatalf("tokens %d want 4000", mm.TotalTokens())
	}
	// Second turn with 8431 tokens => total 12431
	turn2 := &daemon.ExecuteTurnResult{
		Messages: []daemon.MessageResult{{Seq: 2, Role: "assistant", Content: "hi2", Usage: &daemon.UsageResult{TotalTokens: 8431}}},
		Usage:    &daemon.UsageResult{TotalTokens: 8431},
	}
	model, _ = mm.Update(executeTurnResMsg(turn2, nil))
	mm = model.(Model)
	if mm.TotalTokens() != 12431 {
		t.Fatalf("cumulative tokens %d want 12431", mm.TotalTokens())
	}
	view := mm.View().Content
	if !strings.Contains(view, "12,431 tokens") {
		t.Fatalf("footer should show 12,431 tokens, view %q", view[:800])
	}
	// Test formatting helper via footer directly
	pal := MustGetPalette("ember")
	f := components.FooterModel{
		Palette: components.Palette{BG: pal.BG, BGElevated: pal.BGElevated, Border: pal.Border, Text: pal.Text, Dim: pal.Dim, Faint: pal.Faint, Accent: pal.Accent, Success: pal.Success, Warning: pal.Warning, Error: pal.Error},
		Width:   80,
		Tokens:  12431,
	}
	rendered := f.Render()
	if !strings.Contains(rendered, "12,431 tokens") {
		t.Fatalf("footer tokens formatting failed, got %q", rendered)
	}
	// Thousands formatting edge: 1000 -> 1,000
	if formatTokens(1000) != "1,000" || formatTokens(12) != "12" || formatTokens(1000000) != "1,000,000" {
		t.Fatalf("formatTokens broken: %q %q %q", formatTokens(1000), formatTokens(12), formatTokens(1000000))
	}
}

func TestEventStreamClosedShowsToast(t *testing.T) {
	m := newTestModel()
	model, _ := m.Update(eventClosedMsg{})
	mm := model.(Model)
	if !strings.Contains(mm.Toast(), "event stream closed") {
		t.Fatalf("toast %q want event stream closed", mm.Toast())
	}
	if !strings.Contains(mm.daemonErr, "event stream closed") {
		t.Fatalf("daemonErr %q want event stream closed", mm.daemonErr)
	}
}

func TestEmergencyHaltStopsSpinner(t *testing.T) {
	m := newTestModel()
	m.spinner = true
	m.sessionID = "sess-xyz"
	payload := daemon.EmergencyHaltPayload{SessionID: "sess-xyz", Reason: "user"}
	notif := notifPayload(daemon.MethodEmergencyHalt, payload)
	model, _ := m.Update(daemonEventMsg{notif: notif})
	mm := model.(Model)
	if mm.IsSpinner() {
		t.Fatal("emergency halt should stop spinner")
	}
	if !strings.Contains(mm.Toast(), "halted") {
		t.Fatalf("toast %q should contain halted", mm.Toast())
	}
}

func TestFormatTokensDirect(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{12, "12"},
		{123, "123"},
		{1234, "1,234"},
		{12431, "12,431"},
		{1000000, "1,000,000"},
	}
	for _, tc := range cases {
		if got := formatTokens(tc.n); got != tc.want {
			t.Fatalf("formatTokens(%d)=%q want %q", tc.n, got, tc.want)
		}
	}
}

// Regression: the optimistic local echo must be replaced even when a tool
// event landed after it (mid-turn tool events break position-based matching).
func TestEchoReplacedEvenWithToolEventInBetween(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	fc := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m.SetClock(fc)
	cl := &fakeClient{
		turnRes: &daemon.ExecuteTurnResult{
			Messages: []daemon.MessageResult{
				{Seq: 1, Role: "user", Content: "hello"},
				{Seq: 2, Role: "assistant", Content: "hi"},
			},
		},
	}
	m.SetClient(cl)

	// Send: creates local echo.
	m.input.SetValue("hello")
	model, _ := m.Update(keyPress("enter"))
	m = model.(Model)

	// Mid-turn tool event lands AFTER the echo.
	model, _ = m.Update(toolCallStartedNotif("sess-xyz", "call-1", "fs_read"))
	m = model.(Model)
	if len(m.Entries()) != 2 {
		t.Fatalf("expected echo + pending tool, got %+v", m.Entries())
	}

	// Turn result arrives: echo must be absorbed, no duplicate "hello".
	model, _ = m.Update(executeTurnResMsg(cl.turnRes, nil))
	mm := model.(Model)
	got := mm.Entries()
	if len(got) != 3 {
		t.Fatalf("entries after turn = %d (echo duplicated?): %+v", len(got), got)
	}
	// Order: the confirmed user message must sit BEFORE the tool line that
	// interleaved mid-turn (in-place echo replacement preserves chronology).
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Fatalf("first entry should be the confirmed user message, got %+v", got[0])
	}
	if !got[1].IsTool {
		t.Fatalf("second entry should be the tool line, got %+v", got[1])
	}
	helloCount := 0
	for _, e := range got {
		if e.Role == "user" && e.Content == "hello" {
			helloCount++
			if e.Local {
				t.Fatal("confirmed copy must not keep Local flag")
			}
		}
	}
	if helloCount != 1 {
		t.Fatalf("user message rendered %d times, want 1: %+v", helloCount, got)
	}
	if mm.lastSeq != 2 {
		t.Fatalf("lastSeq = %d, want 2", mm.lastSeq)
	}
}

// Regression: message-level dedup must not drop tool lines of later turns
// that reuse a tool name from an earlier turn.
func TestSecondTurnSameToolNameNotDropped(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"

	turn := func(userSeq, asstSeq int) *daemon.ExecuteTurnResult {
		asst := daemon.MessageResult{Seq: asstSeq, Role: "assistant", Content: ""}
		asst.ToolCalls = []daemon.ToolCallResult{}
		asst.ToolCalls = append(asst.ToolCalls, daemon.ToolCallResult{ID: "c", Type: "function"})
		asst.ToolCalls[0].Function.Name = "fs_read"
		return &daemon.ExecuteTurnResult{
			Messages: []daemon.MessageResult{
				{Seq: userSeq, Role: "user", Content: "run again"},
				asst,
			},
		}
	}

	model, _ := m.Update(executeTurnResMsg(turn(1, 2), nil))
	mm := model.(Model)
	model, _ = mm.Update(executeTurnResMsg(turn(3, 4), nil))
	mm = model.(Model)

	toolLines := 0
	for _, e := range mm.Entries() {
		if e.IsTool {
			toolLines++
		}
	}
	if toolLines != 2 {
		t.Fatalf("expected 2 tool lines (one per turn), got %d: %+v", toolLines, mm.Entries())
	}
	if mm.lastSeq != 4 {
		t.Fatalf("lastSeq = %d, want 4", mm.lastSeq)
	}
}

// Regression: a live user message must absorb the local echo immediately
// (executeTurnMsg may arrive much later, or never, on a halt).
func TestLiveUserEventAbsorbsEcho(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"

	m.input.SetValue("hello")
	model, _ := m.Update(keyPress("enter"))
	m = model.(Model)
	if len(m.Entries()) != 1 || !m.Entries()[0].Local {
		t.Fatalf("expected local echo, got %+v", m.Entries())
	}

	model, _ = m.Update(messageEventNotif("sess-xyz", 1, "user", "hello"))
	mm := model.(Model)
	if len(mm.Entries()) != 1 {
		t.Fatalf("echo + live user message both rendered: %+v", mm.Entries())
	}
	if mm.Entries()[0].Local || mm.Entries()[0].Seq != 1 {
		t.Fatalf("single entry should be the confirmed copy: %+v", mm.Entries()[0])
	}
}
