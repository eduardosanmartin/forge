// Package client implements forge's daemon clients: a reconnecting
// JSON-RPC-over-WebSocket transport, an interactive REPL session runner, and
// a non-interactive one-shot mode with structured JSON output.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

const (
	// replHistoryReplay is how many recent messages `/attach` renders locally.
	replHistoryReplay = 10
)

// REPLOptions configures REPL behavior.
type REPLOptions struct {
	EnableRetrieval  bool
	EnableCompaction bool
	EnableAnchoring  bool
	EnableRouting    bool
}

// REPL is an interactive line-oriented client for one forge session.
type REPL struct {
	client       *Client
	sessionID    string
	out          io.Writer
	in           io.Reader
	v1Enabled    REPLOptions

	writeMu sync.Mutex // serializes banner/turn output vs async event lines
}

// NewREPL creates a REPL bound to sessionID. An empty sessionID makes Run
// create a fresh session on startup.
func NewREPL(cl *Client, sessionID string, out io.Writer, in io.Reader, opts REPLOptions) *REPL {
	return &REPL{client: cl, sessionID: sessionID, out: out, in: in, v1Enabled: opts}
}

// Run drives the read-eval loop until /exit, Ctrl-D (EOF), or context
// cancellation. Prompt errors are reported inline; only fatal setup problems
// abort the loop.
func (r *REPL) Run(ctx context.Context) error {
	if err := r.ensureSession(ctx); err != nil {
		return err
	}

	eventsCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()
	eventCh, err := r.client.Events(eventsCtx)
	if err != nil {
		return fmt.Errorf("subscribe to daemon events: %w", err)
	}
	go r.drainEvents(eventCh)

	r.writef("forge REPL - session %s\n", r.sessionID)
	r.writef("V1 features: retrieval=%v compaction=%v anchoring=%v routing=%v\n",
		r.v1Enabled.EnableRetrieval, r.v1Enabled.EnableCompaction, r.v1Enabled.EnableAnchoring, r.v1Enabled.EnableRouting)
	r.writef("Type a message, /help for commands, /exit or Ctrl-D to quit.\n")

	scanner := bufio.NewScanner(r.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		r.write("> ")
		if !scanner.Scan() {
			// EOF (Ctrl-D) or reader error ends the session gracefully.
			if serr := scanner.Err(); serr != nil && serr != io.EOF {
				return fmt.Errorf("read input: %w", serr)
			}
			return ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		switch {
		case line == "/exit":
			r.writeln("bye.")
			return nil
		case line == "/help":
			r.printHelp()
		case strings.HasPrefix(line, "/model"):
			r.cmdModel(ctx, line)
		case strings.HasPrefix(line, "/sessions"):
			r.cmdSessions(ctx)
		case strings.HasPrefix(line, "/new"):
			r.cmdNew(ctx)
		case strings.HasPrefix(line, "/attach"):
			r.cmdAttach(ctx, line)
		case strings.HasPrefix(line, "/halt"):
			r.cmdHalt(ctx, line)
		case strings.HasPrefix(line, "/resume"):
			r.cmdResume(ctx, line)
		default:
			r.cmdTurn(ctx, line)
		}
	}
}

// ensureSession validates the configured session or creates a new one.
func (r *REPL) ensureSession(ctx context.Context) error {
	if r.sessionID == "" {
		var res daemon.SessionResult
		if err := r.client.Call(ctx, daemon.MethodCreateSession,
			daemon.CreateSessionParams{Metadata: map[string]any{"source": "repl"}}, &res); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		r.sessionID = res.ID
		return nil
	}
	var res daemon.SessionResult
	if err := r.client.Call(ctx, daemon.MethodGetSession,
		daemon.GetSessionParams{SessionID: r.sessionID}, &res); err != nil {
		if IsCode(err, daemon.ErrCodeSessionNotFound) {
			return fmt.Errorf("session %s not found (start one with /new)", r.sessionID)
		}
		return fmt.Errorf("open session %s: %w", r.sessionID, err)
	}
	return nil
}

// drainEvents surfaces asynchronous daemon notifications inline.
func (r *REPL) drainEvents(ch <-chan daemon.JSONRPCNotification) {
	for notif := range ch {
		switch notif.Method {
		case daemon.MethodEmergencyHalt:
			var p daemon.EmergencyHaltPayload
			_ = json.Unmarshal(notif.Params, &p)
			if p.SessionID == "" {
				r.writef("\n! EMERGENCY HALT (%s): all sessions stopped\n> ", p.Reason)
			} else {
				r.writef("\n! EMERGENCY HALT (%s): session %s\n> ", p.Reason, p.SessionID)
			}
		default:
			// Other notifications are rendered as part of turn results (v0).
		}
	}
}

func (r *REPL) printHelp() {
	r.writeln("Commands:")
	r.writeln("  /model <name>   hot-swap the default LLM model")
	r.writeln("  /sessions       list sessions")
	r.writeln("  /new            start a new session")
	r.writeln("  /attach <id>    switch to an existing session (replays last messages)")
	r.writeln("  /halt [id]      emergency-halt current or given session")
	r.writeln("  /resume <id>    resume a halted session")
	r.writeln("  /exit           quit (Ctrl-D works too)")
	r.writeln("")
	r.writeln("V1 flags (set at startup):")
	r.writeln("  --retrieval    Enable selective context retrieval")
	r.writeln("  --compaction   Enable hierarchical compaction")
	r.writeln("  --anchoring    Enable persistent anchored facts")
	r.writeln("  --routing      Enable cost-based model routing")
}

func (r *REPL) cmdModel(ctx context.Context, line string) {
	arg, ok := splitCommandArg(line)
	if !ok {
		r.writeln("usage: /model <name>")
		return
	}
	var res struct {
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
	}
	if err := r.client.Call(ctx, daemon.MethodSwitchModel,
		daemon.SwitchModelParams{SessionID: r.sessionID, Model: arg}, &res); err != nil {
		r.writef("error: %v\n", err)
		return
	}
	r.writef("model switched to %s\n", arg)
}

func (r *REPL) cmdSessions(ctx context.Context) {
	var res daemon.ListSessionsResult
	if err := r.client.Call(ctx, daemon.MethodListSessions,
		daemon.ListSessionsParams{Limit: 50}, &res); err != nil {
		r.writef("error: %v\n", err)
		return
	}
	r.writef("%-38s %-17s %6s %s\n", "SESSION", "CREATED", "MSGS", "MODEL")
	for _, s := range res.Sessions {
		model := "-"
		if m, ok := s.Metadata["model"].(string); ok && m != "" {
			model = m
		}
		r.writef("%-38s %-17s %6d %s\n",
			s.ID, formatTimestamp(s.CreatedAt), s.MessageCount, model)
	}
}

func (r *REPL) cmdNew(ctx context.Context) {
	var res daemon.SessionResult
	if err := r.client.Call(ctx, daemon.MethodCreateSession,
		daemon.CreateSessionParams{Metadata: map[string]any{"source": "repl"}}, &res); err != nil {
		r.writef("error: %v\n", err)
		return
	}
	r.sessionID = res.ID
	r.writef("switched to new session %s\n", r.sessionID)
}

func (r *REPL) cmdAttach(ctx context.Context, line string) {
	arg, ok := splitCommandArg(line)
	if !ok {
		r.writeln("usage: /attach <id>")
		return
	}
	var sess daemon.SessionResult
	if err := r.client.Call(ctx, daemon.MethodGetSession,
		daemon.GetSessionParams{SessionID: arg}, &sess); err != nil {
		if IsCode(err, daemon.ErrCodeSessionNotFound) {
			r.writef("error: session %s not found\n", arg)
			return
		}
		r.writef("error: %v\n", err)
		return
	}

	var msgs daemon.GetMessagesResult
	if err := r.client.Call(ctx, daemon.MethodGetMessages,
		daemon.GetMessagesParams{SessionID: arg, Limit: replHistoryReplay}, &msgs); err == nil {
		r.writef("attached to %s (last %d message(s))\n", arg, len(msgs.Messages))
		for _, m := range msgs.Messages {
			r.writef("[%s] %s\n", m.Role, oneLine(m.Content))
		}
	} else {
		r.writef("attached to %s (history unavailable: %v)\n", arg, err)
	}
	r.sessionID = arg
}

func (r *REPL) cmdHalt(ctx context.Context, line string) {
	target := r.sessionID
	if arg, ok := splitCommandArg(line); ok {
		target = arg
	}
	if target == "" {
		if err := r.client.Call(ctx, daemon.MethodHaltAll, nil, nil); err != nil {
			r.writef("error: %v\n", err)
			return
		}
		r.writeln("global halt issued.")
		return
	}
	if err := r.client.Call(ctx, daemon.MethodHaltSession,
		daemon.HaltSessionParams{SessionID: target, Reason: "user"}, nil); err != nil {
		r.writef("error: %v\n", err)
		return
	}
	r.writef("halted %s\n", target)
}

func (r *REPL) cmdResume(ctx context.Context, line string) {
	arg, ok := splitCommandArg(line)
	if !ok {
		r.writeln("usage: /resume <id>")
		return
	}
	if err := r.client.Call(ctx, daemon.MethodResumeSession,
		daemon.ResumeSessionParams{SessionID: arg}, nil); err != nil {
		r.writef("error: %v\n", err)
		return
	}
	r.writef("resumed %s\n", arg)
}

// cmdTurn executes one agent turn and renders the outcome.
func (r *REPL) cmdTurn(ctx context.Context, line string) {
	var res daemon.ExecuteTurnResult
	err := r.client.Call(ctx, daemon.MethodExecuteTurn,
		daemon.ExecuteTurnParams{
			SessionID:        r.sessionID,
			UserMessage:      line,
			EnableRetrieval:  r.v1Enabled.EnableRetrieval,
			EnableCompaction: r.v1Enabled.EnableCompaction,
			EnableAnchoring:  r.v1Enabled.EnableAnchoring,
			EnableRouting:    r.v1Enabled.EnableRouting,
		}, &res)
	if err != nil {
		if IsCode(err, daemon.ErrCodeSessionHalted) {
			r.writef("session is halted: %v (use /resume %s)\n", err, r.sessionID)
			return
		}
		r.writef("error: %v\n", err)
		return
	}

	renderToolTrace(r.out, res.ToolTrace)
	if strings.TrimSpace(res.FinalContent) == "" {
		r.writeln("(assistant returned no content)")
	} else {
		r.writef("%s\n", res.FinalContent)
	}
}

func renderToolTrace(out io.Writer, trace []daemon.ToolTraceResult) {
	for _, tc := range trace {
		fmt.Fprintf(out, "-> %s(%s)\n", tc.Name, FormatToolArgs(tc.Args))
		if tc.OK {
			fmt.Fprintln(out, "<- ok")
		} else {
			fmt.Fprintln(out, "<- error")
		}
	}
}

func splitCommandArg(line string) (string, bool) {
	fields := strings.SplitN(line, " ", 2)
	if len(fields) < 2 {
		return "", false
	}
	arg := strings.TrimSpace(fields[1])
	return arg, arg != ""
}

func formatTimestamp(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	const maxLen = 100
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func (r *REPL) write(s string) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, _ = io.WriteString(r.out, s)
}

func (r *REPL) writef(format string, args ...any) {
	r.write(fmt.Sprintf(format, args...))
}

func (r *REPL) writeln(s string) {
	r.write(s + "\n")
}