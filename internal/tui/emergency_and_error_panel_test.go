package tui

import (
	"strings"
	"testing"
	"time"
)

// TestErrorPanel_OpensWithFullMessageAndEscCloses confirms an RPC error
// (the trigger case: switching to an unavailable model) opens the floating
// error panel with the FULL, untruncated message — not the footer toast,
// which would cut off or crowd a long "rpc error -32602: model unavailable
// (...)" string — and that esc closes it.
func TestErrorPanel_OpensWithFullMessageAndEscCloses(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	longErr := `rpc error -32602: model unavailable (model "qwen3.5-9b-ctx16k" not available: model "qwen3.5-9b-ctx16k" not available in provider "ollama")`
	fc := &fakeClient{switchErr: fakeErr(longErr)}
	m.SetClient(fc)
	m.input.SetValue("/model qwen3.5-9b-ctx16k")
	model, cmd := m.Update(keyPress("enter"))
	mm := model.(Model)
	if cmd == nil {
		t.Fatal("expected a switch-model RPC command")
	}
	msg := cmd()
	res, ok := msg.(switchModelResultMsg)
	if !ok {
		t.Fatalf("expected switchModelResultMsg, got %T", msg)
	}
	model, _ = mm.Update(res)
	mm = model.(Model)

	if !mm.IsMessagePanelVisible() {
		t.Fatal("model-switch error should open the error panel")
	}
	if mm.MessagePanelText() != longErr {
		t.Fatalf("error panel text = %q, want the full untruncated error %q", mm.MessagePanelText(), longErr)
	}
	if !strings.Contains(mm.View().Content, "qwen3.5-9b-ctx16k") {
		t.Fatal("rendered view should show the error panel content")
	}

	model, _ = mm.Update(keyPress("esc"))
	mm = model.(Model)
	if mm.IsMessagePanelVisible() {
		t.Fatal("esc should close the error panel")
	}
}

// TestEmergencyStop_DoubleEscTriggersHaltAll confirms two consecutive esc
// presses within the double-tap window call the daemon's global
// emergency.halt_all, not just HaltSession — the whole point of the
// gesture is to stop every session, not just the current one.
func TestEmergencyStop_DoubleEscTriggersHaltAll(t *testing.T) {
	m := newTestModel()
	m.sessionID = "sess-xyz"
	fc := &fakeClient{}
	m.SetClient(fc)
	clk := newFakeClock(time.Now())
	m.SetClock(clk)

	model, cmd := m.Update(keyPress("esc"))
	mm := model.(Model)
	if cmd != nil {
		t.Fatal("first esc alone should not trigger emergency stop")
	}
	if fc.haltAllCalled {
		t.Fatal("first esc alone should not call HaltAll")
	}

	clk.Advance(100 * time.Millisecond) // well within escDoubleTapWindow
	model, cmd = mm.Update(keyPress("esc"))
	mm = model.(Model)
	if cmd == nil {
		t.Fatal("second esc within the double-tap window should trigger emergency stop")
	}
	msg := cmd()
	res, ok := msg.(haltAllResultMsg)
	if !ok {
		t.Fatalf("expected haltAllResultMsg, got %T", msg)
	}
	model, _ = mm.Update(res)
	mm = model.(Model)
	if !fc.haltAllCalled {
		t.Fatal("HaltAll should have been called on the client")
	}
	if !mm.IsMessagePanelVisible() || mm.MessagePanelKind() != "success" {
		t.Fatalf("expected a success message panel, visible=%v kind=%q", mm.IsMessagePanelVisible(), mm.MessagePanelKind())
	}
	if !strings.Contains(strings.ToLower(mm.MessagePanelText()), "emergency stop") {
		t.Fatalf("message panel should confirm the emergency stop, got %q", mm.MessagePanelText())
	}
}

// TestEmergencyStop_SlowSecondEscDoesNotTrigger confirms two esc presses
// OUTSIDE the double-tap window are just two independent single presses —
// no emergency stop.
func TestEmergencyStop_SlowSecondEscDoesNotTrigger(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{}
	m.SetClient(fc)
	clk := newFakeClock(time.Now())
	m.SetClock(clk)

	model, _ := m.Update(keyPress("esc"))
	mm := model.(Model)

	clk.Advance(2 * time.Second) // past escDoubleTapWindow
	model, cmd := mm.Update(keyPress("esc"))
	mm = model.(Model)
	if cmd != nil {
		t.Fatal("a slow second esc should not trigger emergency stop")
	}
	_ = mm
	if fc.haltAllCalled {
		t.Fatal("HaltAll should not have been called")
	}
}

// TestEmergencyStop_OtherKeyBetweenEscsResetsTheCounter confirms the
// double-tap must be two CONSECUTIVE esc presses — a key in between (even
// a fast one) resets it, so normal typing near an esc press can't
// accidentally arm the emergency stop.
func TestEmergencyStop_OtherKeyBetweenEscsResetsTheCounter(t *testing.T) {
	m := newTestModel()
	fc := &fakeClient{}
	m.SetClient(fc)
	clk := newFakeClock(time.Now())
	m.SetClock(clk)

	model, _ := m.Update(keyPress("esc"))
	mm := model.(Model)
	clk.Advance(10 * time.Millisecond)
	model, _ = mm.Update(keyPress("a"))
	mm = model.(Model)
	clk.Advance(10 * time.Millisecond)
	model, cmd := mm.Update(keyPress("esc"))
	mm = model.(Model)
	if cmd != nil {
		t.Fatal("esc, other key, esc should not trigger emergency stop")
	}
	_ = mm
	if fc.haltAllCalled {
		t.Fatal("HaltAll should not have been called")
	}
}
