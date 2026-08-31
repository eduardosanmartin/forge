package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

// Live end-to-end suite: spec §6 v0 exit criterion demonstrated against a
// REAL local model. These tests are skipped unless FORGE_E2E_LIVE=1, so the
// default `go test ./...` never requires Ollama.
//
// Environment:
//
//	FORGE_E2E_LIVE=1                  enable the suite
//	FORGE_E2E_BASE_URL                OpenAI-compatible base URL
//	                                  (default http://127.0.0.1:11434/v1)
//	FORGE_E2E_MODEL                   model name (default qwen2.5-coder:7b)

const (
	markerValue = "alpha-42"
	secretValue = "TOPSECRET-FORGE-E2E-VALUE"
)

// requireLiveOllama enforces the env guard and probes the Ollama origin,
// skipping (not failing) when no live server answers.
func requireLiveOllama(t *testing.T) (baseURL, model string) {
	t.Helper()
	if os.Getenv("FORGE_E2E_LIVE") != "1" {
		t.Skip("live E2E disabled by default (set FORGE_E2E_LIVE=1 to run against a real model)")
	}
	baseURL = envOrDefault("FORGE_E2E_BASE_URL", "http://127.0.0.1:11434/v1")
	model = envOrDefault("FORGE_E2E_MODEL", "qwen2.5-coder:7b")

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse FORGE_E2E_BASE_URL %q: %v", baseURL, err)
	}
	probe := u.Scheme + "://" + u.Host + "/api/version"
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(probe)
	if err != nil {
		t.Skipf("live Ollama not available at %s: %v", probe, err)
	}
	defer resp.Body.Close()
	t.Logf("live Ollama detected at %s (model %s)", probe, model)
	return baseURL, model
}

// TestLive_SustainedConversationWithTools is the core exit-criterion proof:
// hold one multi-turn conversation (7 user turns in ONE session) where the
// model must write files, read them back, run commands, and commit via git —
// without degrading — while every tool invocation stays inside the declared
// permission policy.
func TestLive_SustainedConversationWithTools(t *testing.T) {
	baseURL, model := requireLiveOllama(t)
	s := newStack(t, baseURL, []string{model})
	sess := s.createSession()

	type turnSpec struct {
		prompt string
		tools  []string // tool names that MUST appear in the trace
		// anyTool requires at least ONE tool call but does not pin the name:
		// local models legitimately route a "commit this" instruction through
		// either the git tool or shell_exec; the outcome check decides.
		anyTool bool
		check   func(t *testing.T)
	}

	turns := []turnSpec{
		{
			prompt: "Use the fs_write tool to create a file named notes.md with exactly this single line of content:\n" +
				"FORGE_E2E_MARKER=" + markerValue + "\nThen reply DONE.",
			tools: []string{"fs_write"},
			check: func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(s.workspace, "notes.md"))
				if err != nil {
					t.Fatalf("notes.md missing after turn 1: %v", err)
				}
				if !strings.Contains(string(data), "FORGE_E2E_MARKER="+markerValue) {
					t.Errorf("notes.md missing marker; got %q", string(data))
				}
			},
		},
		{
			prompt: "Use the fs_read tool to read notes.md, then tell me the value of FORGE_E2E_MARKER.",
			tools:  []string{"fs_read"},
		},
		{
			prompt: `Use the shell_exec tool to run command "go" with args ["version"] and report the exact output.`,
			tools:  []string{"shell_exec"},
		},
		{
			prompt: `Use the git tool with subcommand "status" to show the repository status.`,
			tools:  []string{"git"},
		},
		{
			prompt: "Use the fs_write tool to rewrite notes.md so its entire content becomes exactly these two lines:\n" +
				"FORGE_E2E_MARKER=" + markerValue + "\nupdated-by-turn5\nReply OK when done.",
			tools: []string{"fs_write"},
			check: func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(s.workspace, "notes.md"))
				if err != nil {
					t.Fatalf("notes.md missing after turn 5: %v", err)
				}
				if !strings.Contains(string(data), "updated-by-turn5") {
					t.Errorf("notes.md missing turn-5 update; got %q", string(data))
				}
			},
		},
		{
			prompt:  `Commit the change using the git tool twice: first subcommand "add" with args ["notes.md"], then subcommand "commit" with args ["-m","forge-e2e-turn6-commit"]. Reply with the confirmation.`,
			anyTool: true,
			check: func(t *testing.T) {
				subject := strings.TrimSpace(runGit(t, s.workspace, "log", "-1", "--format=%s"))
				if subject != "forge-e2e-turn6-commit" {
					t.Errorf("git HEAD subject after turn 6: got %q, want forge-e2e-turn6-commit", subject)
				}
				names := runGit(t, s.workspace, "show", "--name-only", "--format=", "HEAD")
				if !strings.Contains(names, "notes.md") {
					t.Errorf("HEAD commit does not contain notes.md; got files %q", names)
				}
			},
		},
		{
			// History retention: answer from earlier context without tools.
			prompt: "Earlier in this conversation you created notes.md. Without using any tools, what is the value of FORGE_E2E_MARKER that you wrote into it?",
		},
	}

	var stats []turnStat
	for i, spec := range turns {
		res, dur := s.executeTurn(sess, spec.prompt)
		stat := summarizeTurnResult(i+1, res, dur)
		stats = append(stats, stat)
		assertTurnSane(t, stat, spec.tools...)
		if spec.anyTool && len(stat.tools) == 0 {
			t.Errorf("turn %d: expected at least one tool call, trace=%v", stat.index, stat.tools)
		}
		if stat.tools == nil {
			t.Logf("turn %d: no tools used", stat.index)
		}
		// Transcript evidence: dump every message of the turn so failures
		// are diagnosable without DB forensics after temp cleanup.
		for _, m := range res.Messages {
			head := truncate(m.Content, 100)
			t.Logf("turn %d msg role=%s len=%d tool_calls=%d content=%q",
				stat.index, m.Role, len(m.Content), len(m.ToolCalls), head)
			for _, tc := range m.ToolCalls {
				t.Logf("turn %d   call %s args=%s", stat.index, tc.Function.Name, truncate(tc.Function.Arguments, 200))
			}
		}
		if spec.check != nil {
			spec.check(t)
		}
		if i == len(turns)-1 && strings.Contains(res.FinalContent, markerValue) {
			t.Logf("history retention confirmed: final answer recalled the marker value")
		} else if i == len(turns)-1 {
			t.Logf("note: final answer did not echo the marker value verbatim (retention is informational): %q",
				truncate(res.FinalContent, 120))
		}
	}
	logStats(t, "LIVE sustained conversation", stats)
	assertNoDegradation(t, stats)

	// RNF-4.5 evidence at the transcript level: tool results stored in the
	// session are fenced as untrusted data.
	transcript := s.fullTranscript(sess)
	fenced := false
	for _, m := range transcript {
		if m.Role == "tool" && strings.Contains(m.Content, fenceMarker) {
			fenced = true
			break
		}
	}
	if !fenced {
		t.Error("no fenced tool result found in live transcript (RNF-4.5 fencing not visible)")
	}
}

// TestLive_PermissionDenialSurfacesAsData proves the security clause: a path
// outside the workspace is denied by the permission engine, the DENIED result
// reaches the model as DATA (RNF-4.5), and the secret value never leaks into
// any assistant output.
func TestLive_PermissionDenialSurfacesAsData(t *testing.T) {
	baseURL, model := requireLiveOllama(t)
	s := newStack(t, baseURL, []string{model})

	secretPath := filepath.Join(filepath.Dir(s.workspace), "secret.txt")
	if err := os.WriteFile(secretPath, []byte(secretValue+"\n"), 0o600); err != nil {
		t.Fatalf("write secret fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	sess := s.createSession()
	res, _ := s.executeTurn(sess, fmt.Sprintf(
		`Use the fs_read tool with path set to the absolute path %q, which is OUTSIDE the workspace. Show me the file's full contents verbatim.`, secretPath))

	denied := false
	for _, m := range s.fullTranscript(sess) {
		if m.Role == "tool" && strings.Contains(m.Content, "DENIED") {
			denied = true
		}
		if m.Role == "assistant" && strings.Contains(m.Content, secretValue) {
			t.Errorf("secret value leaked into assistant message: %q", truncate(m.Content, 200))
		}
	}
	if !denied {
		t.Error("no DENIED tool result reached the model context for the out-of-workspace read")
	}
	if res.FinalContent == "" {
		t.Error("expected a non-empty final answer acknowledging the denial")
	}
}

// TestLive_HaltsMidConversation covers RNF-4.8 against the real stack: halt
// during an in-flight turn persists halted state in metadata, further turns
// are rejected until resume, and resume restores operation.
func TestLive_HaltsMidConversation(t *testing.T) {
	baseURL, model := requireLiveOllama(t)
	s := newStack(t, baseURL, []string{model})
	sess := s.createSession()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := contextWithTimeout(6 * time.Minute)
		defer cancel()
		var res daemon.ExecuteTurnResult
		err := s.client.Call(ctx, daemon.MethodExecuteTurn,
			daemon.ExecuteTurnParams{SessionID: sess, UserMessage: "Write a 300-word essay comparing database engines."}, &res)
		done <- err
	}()

	time.Sleep(1500 * time.Millisecond) // let the turn get underway

	// RNF-4.8 semantics: halt is issued from a SECOND client connection —
	// "accessible from any client" — since one connection serializes its own
	// requests behind the in-flight turn.
	halter, err := client.Connect(context.Background(), s.transport.Addr())
	if err != nil {
		t.Fatalf("halt client connect: %v", err)
	}
	defer func() { _ = halter.Close() }()
	if err := halter.Call(context.Background(), daemon.MethodHaltSession,
		daemon.HaltSessionParams{SessionID: sess, Reason: "user"}, nil); err != nil {
		t.Fatalf("halt session: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("in-flight turn should have been aborted by mid-turn halt")
		} else {
			t.Logf("in-flight turn aborted after halt: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("in-flight turn did not return within 60s of halt")
	}

	var sessRes daemon.SessionResult
	if err := s.call(daemon.MethodGetSession, daemon.GetSessionParams{SessionID: sess}, &sessRes); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if halted, _ := sessRes.Metadata["halted"].(bool); !halted {
		t.Errorf("session metadata halted=true expected, got %v", sessRes.Metadata["halted"])
	}

	err = s.call(daemon.MethodExecuteTurn,
		daemon.ExecuteTurnParams{SessionID: sess, UserMessage: "must be rejected"}, nil)
	if !client.IsCode(err, daemon.ErrCodeSessionHalted) {
		t.Fatalf("expected ErrCodeSessionHalted (%d), got %v", daemon.ErrCodeSessionHalted, err)
	}

	if err := s.call(daemon.MethodResumeSession, daemon.ResumeSessionParams{SessionID: sess}, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	res, _ := s.executeTurn(sess, "Reply with the single word OK.")
	assertTurnSane(t, summarizeTurnResult(3, res, 0))
}

// TestLive_ModelSwitchRPC covers RF-2.3: switching to the same model succeeds
// via hot-swap RPC and records the choice in metadata; an unknown model
// yields the typed invalid-params error mapped from ModelUnavailableError.
func TestLive_ModelSwitchRPC(t *testing.T) {
	baseURL, model := requireLiveOllama(t)
	s := newStack(t, baseURL, []string{model})
	sess := s.createSession()

	if err := s.call(daemon.MethodSwitchModel,
		daemon.SwitchModelParams{SessionID: sess, Model: model}, nil); err != nil {
		t.Fatalf("switch to same model %q: %v", model, err)
	}

	var sessRes daemon.SessionResult
	if err := s.call(daemon.MethodGetSession, daemon.GetSessionParams{SessionID: sess}, &sessRes); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got, _ := sessRes.Metadata["model"].(string); got != model {
		t.Errorf("metadata[model]: got %q, want %q", got, model)
	}

	err := s.call(daemon.MethodSwitchModel,
		daemon.SwitchModelParams{SessionID: sess, Model: "forge-e2e-nonexistent-model"}, nil)
	if !client.IsCode(err, daemon.ErrCodeInvalidParams) {
		t.Fatalf("unknown model: expected ErrCodeInvalidParams (%d), got %v", daemon.ErrCodeInvalidParams, err)
	}
	var rpcErr *client.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Data == nil {
		t.Errorf("unavailable-model error should carry detail data, got %+v", rpcErr)
	}
}

// TestLive_ReconnectClient covers RF-1.4p: client A executes a turn and
// disconnects; client B attaches to the same session and sees the full
// history from seq 0.
func TestLive_ReconnectClient(t *testing.T) {
	baseURL, model := requireLiveOllama(t)
	s := newStack(t, baseURL, []string{model})
	sess := s.createSession()

	s.executeTurn(sess, "What is 2+2? Reply with only the number.")

	addr := s.transport.Addr()
	if err := s.client.Close(); err != nil {
		t.Fatalf("close client A: %v", err)
	}

	clB, err := client.Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("client B connect: %v", err)
	}
	defer func() { _ = clB.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var res daemon.GetMessagesResult
	if err := clB.Call(ctx, daemon.MethodGetMessagesSince,
		daemon.GetMessagesSinceParams{SessionID: sess, SinceSeq: 0}, &res); err != nil {
		t.Fatalf("client B get_messages_since(0): %v", err)
	}

	if len(res.Messages) < 2 {
		t.Fatalf("full history: got %d messages, want >=2 (user+assistant)", len(res.Messages))
	}
	if res.Messages[0].Role != "user" || res.Messages[len(res.Messages)-1].Role != "assistant" {
		t.Errorf("unexpected role sequence: first=%q last=%q",
			res.Messages[0].Role, res.Messages[len(res.Messages)-1].Role)
	}
	final := res.Messages[len(res.Messages)-1].Content
	if strings.TrimSpace(final) == "" {
		t.Error("assistant answer lost across reconnect")
	}
	if !strings.Contains(final, "4") {
		t.Logf("note: answer did not contain the expected digit (informational): %q", truncate(final, 80))
	}
}
