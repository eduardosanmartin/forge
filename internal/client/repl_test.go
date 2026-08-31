package client

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/store"
)

// runREPL feeds script to a REPL bound to the stack and returns everything
// it wrote. Every run is deadline-bounded so a regression cannot wedge the
// suite.
func runREPL(t *testing.T, stack *daemonStack, sessionID, script string) string {
	t.Helper()
	var out bytes.Buffer
	repl := NewREPL(stack.client, sessionID, &out, strings.NewReader(script), REPLOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := repl.Run(ctx); err != nil {
		t.Fatalf("REPL run failed: %v\noutput so far:\n%s", err, out.String())
	}
	return out.String()
}

func TestREPLBannerAndExit(t *testing.T) {
	stack := startTestDaemon(t)

	out := runREPL(t, stack, "", "/exit\n")
	if !strings.Contains(out, "forge REPL - session sess-001") {
		t.Errorf("banner should name the created session, got:\n%s", out)
	}
	if !strings.Contains(out, "bye.") {
		t.Errorf("exit confirmation missing, got:\n%s", out)
	}
}

func TestREPLAttachToExplicitSession(t *testing.T) {
	stack := startTestDaemon(t)
	sess, err := stack.store.CreateSession(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	out := runREPL(t, stack, sess.ID, "/exit\n")
	if !strings.Contains(out, "session "+sess.ID) {
		t.Errorf("expected banner for pre-set session, got:\n%s", out)
	}
}

func TestREPLEOFExitsGracefully(t *testing.T) {
	stack := startTestDaemon(t)

	var out bytes.Buffer
	repl := NewREPL(stack.client, "", &out, strings.NewReader(""), REPLOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := repl.Run(ctx); err != nil {
		t.Fatalf("EOF should exit gracefully, got %v", err)
	}
}

func TestREPLOnlyNewlinesSkipTurns(t *testing.T) {
	stack := startTestDaemon(t)

	runREPL(t, stack, "", "\n\n   \n/exit\n")
	if n := stack.provider.chatCount(); n != 0 {
		t.Errorf("blank lines must not trigger turns, got %d LLM chats", n)
	}
}

func TestREPLTurnRendersAssistantReply(t *testing.T) {
	stack := startTestDaemon(t, plainReply("Hello from mock"))

	out := runREPL(t, stack, "", "hello there\n/exit\n")
	if !strings.Contains(out, "Hello from mock") {
		t.Errorf("assistant reply not rendered, got:\n%s", out)
	}
	if got := stack.store.userMessageSession("hello there"); got == "" {
		t.Error("user message was never persisted")
	}
}

func TestREPLTurnWithToolTrace(t *testing.T) {
	stack := startTestDaemon(t,
		toolCallReply("reading file",
			llmToolCall("t1", "fs_read", `{"path":"notes.txt"}`)),
		plainReply("final answer after tool"),
	)

	out := runREPL(t, stack, "", "read my notes\n/exit\n")
	for _, want := range []string{
		"-> fs_read(path=notes.txt)",
		"<- ok",
		"final answer after tool",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got:\n%s", want, out)
		}
	}
}

func TestREPLSessionsTableShowsCountsAndModel(t *testing.T) {
	stack := startTestDaemon(t)

	out := runREPL(t, stack, "", "/sessions\n/exit\n")
	for _, want := range []string{"SESSION", "MSGS", "MODEL", "sess-001"} {
		if !strings.Contains(out, want) {
			t.Errorf("sessions table missing %q, got:\n%s", want, out)
		}
	}
}

func TestREPLNewSwitchesSession(t *testing.T) {
	stack := startTestDaemon(t, plainReply("ok"))

	out := runREPL(t, stack, "", "/new\nhi there\n/exit\n")
	if !strings.Contains(out, "switched to new session sess-002") {
		t.Errorf("new-session switch not reported, got:\n%s", out)
	}
	if got := stack.store.userMessageSession("hi there"); got != "sess-002" {
		t.Errorf("turn after /new should target sess-002, landed in %q", got)
	}
}

func TestREPLAttachReplaysHistory(t *testing.T) {
	stack := startTestDaemon(t)

	ctx := context.Background()
	sess, err := stack.store.CreateSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []*store.Message{
		{SessionID: sess.ID, Role: "user", Content: "old question"},
		{SessionID: sess.ID, Role: "assistant", Content: "old answer with detail that is long enough to be truncated maybe but fine"},
	}
	for _, m := range seed {
		if _, _, err := stack.store.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	out := runREPL(t, stack, "", fmt.Sprintf("/attach %s\n/exit\n", sess.ID))
	if !strings.Contains(out, "[user] old question") {
		t.Errorf("history replay missing user line, got:\n%s", out)
	}
	if !strings.Contains(out, "[assistant] old answer") {
		t.Errorf("history replay missing assistant line, got:\n%s", out)
	}
}

func TestREPLModelSwitchPersistsMetadata(t *testing.T) {
	stack := startTestDaemon(t)

	out := runREPL(t, stack, "", "/model model-b\n/exit\n")
	if !strings.Contains(out, "model switched to model-b") {
		t.Errorf("switch confirmation missing, got:\n%s", out)
	}
	if stack.registry.defaultModel != "model-b" {
		t.Errorf("registry default model = %q, want model-b", stack.registry.defaultModel)
	}
	sess, err := stack.store.GetSession(context.Background(), "sess-001")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Metadata["model"] != "model-b" {
		t.Errorf("session metadata model = %v, want model-b", sess.Metadata["model"])
	}
}

func TestREPLModelInvalidRejected(t *testing.T) {
	stack := startTestDaemon(t)

	out := runREPL(t, stack, "", "/model does-not-exist\n/exit\n")
	if !strings.Contains(out, "error:") || !strings.Contains(out, "not available") {
		t.Errorf("invalid model should surface typed error, got:\n%s", out)
	}
	if stack.registry.defaultModel != "model-a" {
		t.Errorf("default model must stay model-a, got %q", stack.registry.defaultModel)
	}
}

func TestREPLHaltResumeCycle(t *testing.T) {
	stack := startTestDaemon(t)

	script := "/halt\n/resume sess-001\n/exit\n"
	out := runREPL(t, stack, "", script)
	if !strings.Contains(out, "halted sess-001") {
		t.Errorf("halt report missing, got:\n%s", out)
	}
	if !strings.Contains(out, "resumed sess-001") {
		t.Errorf("resume report missing, got:\n%s", out)
	}
}

func TestREPLTurnOnHaltedSessionReportsState(t *testing.T) {
	stack := startTestDaemon(t, plainReply("should never render"))

	out := runREPL(t, stack, "", "/halt\ntell me something\n/exit\n")
	if !strings.Contains(out, "session is halted") {
		t.Errorf("halted-turn feedback missing, got:\n%s", out)
	}
	if strings.Contains(out, "should never render") {
		t.Error("halted session executed an LLM turn anyway")
	}
}

func TestREPLHelpListsCommands(t *testing.T) {
	stack := startTestDaemon(t)

	out := runREPL(t, stack, "", "/help\n/exit\n")
	for _, cmd := range []string{"/model", "/sessions", "/new", "/attach", "/halt", "/resume", "/exit"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("/help missing %s, got:\n%s", cmd, out)
		}
	}
}

// llmToolCall builds a scripted assistant tool call.
func llmToolCall(id, name, argsJSON string) llm.ToolCall {
	return llm.ToolCall{
		ID:   id,
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      name,
			Arguments: argsJSON,
		},
	}
}