package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestRunOneShotEphemeralSessionAndResponse(t *testing.T) {
	stack := startTestDaemon(t, plainReply("oneshot says hi"))

	ctx, cancel := callCtx(t)
	defer cancel()
	res, err := RunOneShot(ctx, stack.client, "do the thing", RunOptions{})
	if err != nil {
		t.Fatalf("RunOneShot failed: %v", err)
	}

	if !strings.HasPrefix(res.SessionID, "sess-") {
		t.Errorf("ephemeral session not created, id %q", res.SessionID)
	}
	if res.Response != "oneshot says hi" {
		t.Errorf("response = %q", res.Response)
	}
	if res.Model != "model-a" {
		t.Errorf("model = %q, want model-a", res.Model)
	}
	if res.DurationMs <= 0 {
		t.Errorf("duration must be positive, got %d ms", res.DurationMs)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 18 || res.Usage.PromptTokens != 11 || res.Usage.CompletionTokens != 7 {
		t.Errorf("usage mismatch: %+v", res.Usage)
	}

	sess, err := stack.store.GetSession(context.Background(), res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Metadata["source"] != "oneshot" {
		t.Errorf("ephemeral session metadata source = %v, want oneshot", sess.Metadata["source"])
	}
}

func TestRunOneShotToolTracePopulated(t *testing.T) {
	stack := startTestDaemon(t,
		toolCallReply("reading file",
			llmToolCall("t1", "fs_read", `{"path":"notes.txt"}`)),
		plainReply("done"),
	)

	ctx, cancel := callCtx(t)
	defer cancel()
	res, err := RunOneShot(ctx, stack.client, "read notes", RunOptions{})
	if err != nil {
		t.Fatalf("RunOneShot failed: %v", err)
	}

	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool trace len = %d, want 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.Name != "fs_read" || !tc.OK {
		t.Errorf("trace entry = %+v, want fs_read ok", tc)
	}
	var args map[string]string
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("args should be valid JSON: %v", err)
	}
	if args["path"] != "notes.txt" {
		t.Errorf("args path = %q", args["path"])
	}
}

func TestRunOneShotFailedToolMarkedNotOK(t *testing.T) {
	stack := startTestDaemon(t,
		toolCallReply("attempting",
			llmToolCall("t1", "fs_read", `{"path":"secret"}`)),
		plainReply("recovered anyway"),
	)
	stack.toolsReg.fail = map[string]bool{"fs_read": true}

	ctx, cancel := callCtx(t)
	defer cancel()
	res, err := RunOneShot(ctx, stack.client, "try it", RunOptions{})
	if err != nil {
		t.Fatalf("RunOneShot failed: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].OK {
		t.Errorf("failed tool should be traced with ok=false, got %+v", res.ToolCalls)
	}
	if res.Response != "recovered anyway" {
		t.Errorf("turn should still finish, response %q", res.Response)
	}
}

func TestRunOneShotReuseExistingSession(t *testing.T) {
	stack := startTestDaemon(t, plainReply("in place"))

	sctx, scancel := callCtx(t)
	defer scancel()
	var sess daemon.SessionResult
	if err := stack.client.Call(sctx, daemon.MethodCreateSession, daemon.CreateSessionParams{}, &sess); err != nil {
		t.Fatal(err)
	}
	res, err := RunOneShot(sctx, stack.client, "targeted", RunOptions{SessionID: sess.ID})
	if err != nil {
		t.Fatalf("reuse failed: %v", err)
	}
	if res.SessionID != sess.ID {
		t.Errorf("session reused = %q, want %q", res.SessionID, sess.ID)
	}
	if n := stack.store.sessionCount(); n != 1 {
		t.Errorf("session count = %d, want 1 (no ephemeral session created)", n)
	}
}

func TestRunOneShotMissingSessionFailsWithTypedError(t *testing.T) {
	stack := startTestDaemon(t)

	ctx, cancel := callCtx(t)
	defer cancel()
	_, err := RunOneShot(ctx, stack.client, "hi", RunOptions{SessionID: "ghost"})
	if !IsCode(err, daemon.ErrCodeSessionNotFound) {
		t.Fatalf("want session-not-found typed error, got %v", err)
	}
	if !strings.Contains(err.Error(), "execute turn") {
		t.Errorf("error should name the failing step, got %v", err)
	}
}

func TestOneShotResultJSONShape(t *testing.T) {
	res := &OneShotResult{
		SessionID:  "s1",
		Model:      "m",
		Response:   "answer",
		DurationMs: 42,
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "model", "response", "duration_ms"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("JSON missing required key %q: %s", key, data)
		}
	}
	for _, omittable := range []string{"tool_calls", "usage"} {
		if _, ok := doc[omittable]; ok {
			t.Errorf("empty %q must be omitted, got %s", omittable, data)
		}
	}
	if !strings.Contains(string(data), `"duration_ms": 42`) {
		t.Errorf("duration field shape unexpected: %s", data)
	}
}
