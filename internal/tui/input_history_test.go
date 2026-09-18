package tui

import "testing"

// sendAndComplete types text into the input and sends it (enter), then
// resets the spinner as if the turn had already completed — in real usage
// enter is blocked while a turn is in flight ("turn in flight… ctrl+h to
// halt"), so a test sending more than one message must simulate the
// previous turn finishing, or every send past the first is silently
// swallowed by that guard.
func sendAndComplete(mm Model, text string) Model {
	mm.input.SetValue(text)
	model, _ := mm.Update(keyPress("enter"))
	mm = model.(Model)
	mm.spinner = false
	return mm
}

// TestInputHistory_UpDownBrowsesSentMessages confirms up/down cycle through
// previously sent messages (newest first on the first "up"), and that
// "down" past the newest entry restores whatever was being typed before
// browsing started — the standard shell-history contract (item 20).
func TestInputHistory_UpDownBrowsesSentMessages(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-hist"

	mm := sendAndComplete(m, "first")
	mm = sendAndComplete(mm, "second")

	// Start typing a fresh (unsent) draft.
	mm.input.SetValue("draft")

	model, _ := mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "second" {
		t.Fatalf("first up = %q, want \"second\" (most recent)", got)
	}

	model, _ = mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "first" {
		t.Fatalf("second up = %q, want \"first\"", got)
	}

	// At the oldest entry: another "up" stays put (no wrap, no panic).
	model, _ = mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "first" {
		t.Fatalf("up at oldest entry = %q, want to stay at \"first\"", got)
	}

	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "second" {
		t.Fatalf("down from oldest = %q, want \"second\"", got)
	}

	// One more "down" walks past the newest sent entry, back to the draft
	// that was being typed before history browsing started.
	model, _ = mm.Update(keyPress("down"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "draft" {
		t.Fatalf("down past newest = %q, want the restored draft \"draft\"", got)
	}
}

// TestInputHistory_ConsecutiveDuplicateNotDoubleRecorded confirms sending
// the same text twice in a row only records it once in history (matches
// common shell-history behavior).
func TestInputHistory_ConsecutiveDuplicateNotDoubleRecorded(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-hist"

	mm := sendAndComplete(m, "same")
	mm = sendAndComplete(mm, "same")

	model, _ := mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "same" {
		t.Fatalf("up = %q, want \"same\"", got)
	}
	// A second "up" must stay at the single recorded "same" entry, not a
	// second (duplicate) history slot.
	model, _ = mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "same" {
		t.Fatalf("second up = %q, want to stay at the single \"same\" entry (no duplicate recorded)", got)
	}
}

// TestInputHistory_UpWithinMultilineTextMovesCursorNotHistory confirms
// up/down only start browsing history once the cursor is already at the
// input's first/last line — with a multi-line draft and the cursor NOT at
// line 0, "up" must NOT jump into history (it has real text-navigation
// work to do first).
func TestInputHistory_UpWithinMultilineTextMovesCursorNotHistory(t *testing.T) {
	m := newTestModel()
	m.SetClient(&fakeClient{})
	m.sessionID = "sess-hist"
	mm := sendAndComplete(m, "sent-earlier")

	mm.input.SetValue("line one\nline two")
	if mm.input.TA.Line() != 1 {
		t.Fatalf("cursor should land on the last line (1) after SetValue, got %d", mm.input.TA.Line())
	}
	model, _ := mm.Update(keyPress("up"))
	mm = model.(Model)
	if got := mm.input.Value(); got != "line one\nline two" {
		t.Fatalf("up from line 1 should move the cursor within the text, not browse history — value changed to %q", got)
	}
	if mm.input.TA.Line() != 0 {
		t.Fatalf("cursor should have moved to line 0, got %d", mm.input.TA.Line())
	}
}
