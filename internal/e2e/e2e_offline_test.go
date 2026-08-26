package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

// The offline suite replays the live scenarios against a scripted
// OpenAI-compatible server, so the DEFAULT `go test ./...` run proves the
// full stack end-to-end (transport -> handler -> session manager -> agent ->
// perms -> tools -> store) without requiring a local model.

// fenceMarker is the untrusted-content fencing marker (RNF-4.5) that must be
// visible in every stored tool result.
const fenceMarker = "<<TOOL_RESULT:"

// TestOffline_SustainedConversationWithTools mirrors the live sustained
// conversation with deterministic scripted tool calls across one session.
func TestOffline_SustainedConversationWithTools(t *testing.T) {
	srv := newScriptServer(t, "mock-7b",
		respTool(toolCall("call-1", "fs.write", map[string]any{
			"path": "hello.txt", "content": "line-one\nline-two\n",
		})),
		respFinal("Wrote hello.txt with two lines."),
		respTool(toolCall("call-2", "fs.read", map[string]any{"path": "hello.txt"})),
		respFinal("hello.txt contains line-one and line-two."),
		respTool(toolCall("call-3", "shell.exec", map[string]any{
			"command": "go", "args": []string{"version"},
		})),
		respFinal("Reported the Go toolchain version."),
	)

	s := newStack(t, srv.URL(), []string{"mock-7b"})
	sess := s.createSession()

	type turnSpec struct {
		prompt string
		tools  []string
	}
	turns := []turnSpec{
		{"Create hello.txt with two lines.", []string{"fs.write"}},
		{"Read hello.txt back and summarize it.", []string{"fs.read"}},
		{"Report the Go version using a shell command.", []string{"shell.exec"}},
	}

	var stats []turnStat
	for i, spec := range turns {
		res, dur := s.executeTurn(sess, spec.prompt)
		stat := summarizeTurnResult(i+1, res, dur)
		stats = append(stats, stat)
		assertTurnSane(t, stat, spec.tools...)
	}
	logStats(t, "offline sustained conversation", stats)
	assertNoDegradation(t, stats)

	// Deterministic side effects: the file was written with exact content.
	data, err := os.ReadFile(filepath.Join(s.workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("hello.txt not written by turn 1: %v", err)
	}
	if string(data) != "line-one\nline-two\n" {
		t.Errorf("hello.txt content mismatch: got %q", string(data))
	}

	// RNF-4.5 evidence: the fs.read tool result stored in the session log is
	// fenced as untrusted content.
	transcript := s.fullTranscript(sess)
	fenced := false
	for _, m := range transcript {
		if m.Role == "tool" && strings.Contains(m.Content, fenceMarker) &&
			strings.Contains(m.Content, "</TOOL_RESULT:") {
			fenced = true
			break
		}
	}
	if !fenced {
		t.Error("no fenced tool result found in transcript (RNF-4.5 fencing not visible)")
	}

	if got := srv.chatCount(); got != 6 { // 3 turns x (tool-call + final) iterations
		t.Errorf("scripted chat calls: got %d, want 6", got)
	}
}

// TestOffline_HaltsMidConversation covers RNF-4.8 deterministically: a slow
// LLM response keeps the turn in flight while halt lands, the in-flight turn
// fails, the halted state persists in session metadata, further turns are
// rejected until resume, and the resumed turn succeeds.
func TestOffline_HaltsMidConversation(t *testing.T) {
	srv := newScriptServer(t, "mock-7b",
		delayed(5*time.Second, respFinal("slow answer that never lands")),
		respFinal("post-resume answer"),
	)

	s := newStack(t, srv.URL(), []string{"mock-7b"})
	sess := s.createSession()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := contextWithTimeout(30 * time.Second)
		defer cancel()
		var res daemon.ExecuteTurnResult
		err := s.client.Call(ctx, daemon.MethodExecuteTurn,
			daemon.ExecuteTurnParams{SessionID: sess, UserMessage: "Think for a long time."}, &res)
		done <- err
	}()

	time.Sleep(700 * time.Millisecond) // let the turn register and hit the LLM

	// RNF-4.8 semantics: the emergency stop is issued from a SECOND client
	// connection (any client must be able to halt), because a single daemon
	// connection is serialized — its own in-flight turn blocks any further
	// request sent over it.
	halter, err := client.Connect(context.Background(), s.transport.Addr())
	if err != nil {
		t.Fatalf("halt client connect: %v", err)
	}
	defer func() { _ = halter.Close() }()
	start := time.Now()
	ctxHalt, cancelHalt := contextWithTimeout(15 * time.Second)
	err = halter.Call(ctxHalt, daemon.MethodHaltSession,
		daemon.HaltSessionParams{SessionID: sess, Reason: "user"}, nil)
	cancelHalt()
	if err != nil {
		t.Fatalf("halt session: %v", err)
	}
	t.Logf("halt RPC returned in %s", time.Since(start))

	select {
	case err := <-done:
		if err == nil {
			t.Error("in-flight turn should have been aborted by halt (mock delay keeps it busy)")
		} else {
			t.Logf("in-flight turn aborted after halt: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("in-flight turn did not return within 15s of halt")
	}

	// Halted state persisted in session metadata (RNF-4.8).
	var sessRes daemon.SessionResult
	if err := s.call(daemon.MethodGetSession, daemon.GetSessionParams{SessionID: sess}, &sessRes); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if halted, _ := sessRes.Metadata["halted"].(bool); !halted {
		t.Errorf("session metadata halted=true expected, got %v", sessRes.Metadata["halted"])
	}

	// Next turn rejected with the typed SessionHalted code until resume.
	err = s.call(daemon.MethodExecuteTurn,
		daemon.ExecuteTurnParams{SessionID: sess, UserMessage: "should be rejected"}, nil)
	if !client.IsCode(err, daemon.ErrCodeSessionHalted) {
		t.Fatalf("expected ErrCodeSessionHalted (%d), got %v", daemon.ErrCodeSessionHalted, err)
	}

	if err := s.call(daemon.MethodResumeSession, daemon.ResumeSessionParams{SessionID: sess}, nil); err != nil {
		t.Fatalf("resume session: %v", err)
	}

	res, dur := s.executeTurn(sess, "Reply after resume.")
	stat := summarizeTurnResult(2, res, dur)
	assertTurnSane(t, stat)
	if res.FinalContent != "post-resume answer" {
		t.Errorf("post-resume final content: got %q", res.FinalContent)
	}
}

// TestOffline_ReconnectClientSeesFullHistory covers RF-1.4p: client A runs
// turns, disconnects; client B attaches to the same session and reads the
// complete history from seq 0.
func TestOffline_ReconnectClientSeesFullHistory(t *testing.T) {
	srv := newScriptServer(t, "mock-7b",
		respFinal("first answer"),
		respFinal("second answer"),
	)

	s := newStack(t, srv.URL(), []string{"mock-7b"})
	sess := s.createSession()

	s.executeTurn(sess, "first question")
	s.executeTurn(sess, "second question")

	// Client A leaves; a fresh client attaches to the same daemon endpoint.
	addr := s.transport.Addr()
	if err := s.client.Close(); err != nil {
		t.Fatalf("close client A: %v", err)
	}

	clB, err := client.Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("client B connect: %v", err)
	}
	defer func() { _ = clB.Close() }()

	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	var res daemon.GetMessagesResult
	if err := clB.Call(ctx, daemon.MethodGetMessagesSince,
		daemon.GetMessagesSinceParams{SessionID: sess, SinceSeq: 0}, &res); err != nil {
		t.Fatalf("client B get_messages_since(0): %v", err)
	}

	wantRoles := []string{"user", "assistant", "user", "assistant"}
	if len(res.Messages) < len(wantRoles) {
		t.Fatalf("history length: got %d messages, want at least %d", len(res.Messages), len(wantRoles))
	}
	lastSeq := 0
	for i, m := range res.Messages {
		if i < len(wantRoles) && m.Role != wantRoles[i] {
			t.Errorf("message %d role: got %q, want %q", i, m.Role, wantRoles[i])
		}
		if m.Seq <= lastSeq {
			t.Errorf("message seq not strictly increasing at index %d: %d after %d", i, m.Seq, lastSeq)
		}
		lastSeq = m.Seq
	}
	finals := []string{res.Messages[1].Content, res.Messages[3].Content}
	if finals[0] != "first answer" || finals[1] != "second answer" {
		t.Errorf("assistant answers lost across reconnect: got %q and %q", finals[0], finals[1])
	}
}
