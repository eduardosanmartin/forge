// Package cli implements the forge command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/coder/websocket"
	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/embedding"
	"github.com/eduardosanmartin/forge/internal/isolation"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/retrieval"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
	"github.com/spf13/cobra"
)

// Daemon commands
func init() {
	RootCommand.AddCommand(newServeCommand())
	RootCommand.AddCommand(newAttachCommand())
	RootCommand.AddCommand(newHaltCommand())
	RootCommand.AddCommand(newResumeCommand())
	RootCommand.AddCommand(newSessionsCommand())
	RootCommand.AddCommand(newStatusCommand())
}

func newServeCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the forge daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, ok := AppFromContext(cmd.Context())
			if !ok {
				return fmt.Errorf("app not initialized")
			}

			// Store addr in config temporarily for daemon creation
			originalStoragePath := app.Config.Storage.Path
			if addr != "" {
				// We'll pass addr directly to daemon.New
				_ = addr // avoid unused warning
			}
			_ = originalStoragePath

			return runServe(cmd.Context(), app, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address (host:port)")
	return cmd
}

func newAttachCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Attach to a running session via daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttach(cmd.Context(), args[0])
		},
	}
	return cmd
}

func newHaltCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "halt [session-id]",
		Short: "Send emergency halt (global or per-session)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHalt(cmd.Context(), args)
		},
	}
	return cmd
}

func newResumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a halted session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResume(cmd.Context(), args[0])
		},
	}
	return cmd
}

func newSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List sessions via daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessions(cmd.Context())
		},
	}
	return cmd
}

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context())
		},
	}
	return cmd
}

func runServe(ctx context.Context, app *App, addr string) error {
	// v0 workspace semantics: forge operates on the directory the daemon was
	// launched from. Relative permission patterns ("./**") and tool paths
	// resolve against this root. Storage.Path is the database location, not
	// the workspace, and must never be used as the security boundary root.
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}

	// Build permission engine - convert config.PermissionsPolicy to perms.PermissionsPolicy
	permsPolicy := perms.PermissionsPolicy{
		FS: perms.FSPermissions{
			Read:  app.Config.Permissions.FS.Read,
			Write: app.Config.Permissions.FS.Write,
		},
		Shell: perms.ShellPermissions{
			Allow: app.Config.Permissions.Shell.Allow,
		},
		Git: perms.GitPermissions{
			Allow: app.Config.Permissions.Git.Allow,
		},
	}
	permsEng, err := perms.New(permsPolicy, workspaceRoot, app.Logger)
	if err != nil {
		return fmt.Errorf("create permission engine: %w", err)
	}

	// Open store
	st, err := store.Open(app.Config.Storage.Path)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Create LLM registry
	llmReg, err := llm.New(app.Config, app.Config.Network.AllowedHosts, app.Logger)
	if err != nil {
		return fmt.Errorf("create llm registry: %w", err)
	}
	defer llmReg.Close()

	// v1 feature dependencies. The embedding store is in-memory per daemon
	// process: embedding.NewStore ignores its DSN today (the parameter is
	// reserved for a future persistent backend), so no sibling database
	// file is created in v1 and the retrieval index lives only for this
	// process. The anchor store shares the session database through the
	// raw handle, with its table created alongside the store schema.
	// Construction errors fail fast: a daemon without its declared v1
	// features would silently regress to placeholder behavior.
	embStore, err := embedding.NewStore("")
	if err != nil {
		app.Logger.Error("v1 deps: embedding store construction failed", "error", err)
		return fmt.Errorf("create embedding store: %w", err)
	}
	defer embStore.Close()
	retriever := retrieval.NewRetriever(embStore)
	compactor := compaction.NewCompactor(compaction.Config{})
	if err := anchor.CreateAnchorTable(ctx, st.DB()); err != nil {
		app.Logger.Error("v1 deps: anchors table creation failed", "error", err)
		return fmt.Errorf("create anchors table: %w", err)
	}
	anchorStore := anchor.NewAnchorStoreSQL(st.DB())
	v1Deps := agent.V1Deps{
		Retriever:   retriever,
		Compactor:   compactor,
		AnchorStore: anchorStore,
	}

	// Create tools registry (base five tools + the six v1 feature tools on
	// their real dependencies)
	toolsReg := tools.NewDefaultRegistryWithDeps(permsEng, workspaceRoot, app.Logger, retriever, compactor, anchorStore)

	// OS-level shell isolation (spec RNF-4.7): shell children run through
	// forge itself as an isolation wrapper (Landlock + seccomp on Linux)
	// whenever the platform supports it — defense-in-depth independent of
	// configuration. require_isolation only escalates unavailability into a
	// refusal (Linux); other platforms ignore it and stay permissions-only.
	isoCap := isolation.Capabilities()
	if isoCap.OSIsolation {
		selfExe, exeErr := os.Executable()
		switch {
		case exeErr == nil:
			toolsReg.SetIsolator(tools.NewOsIsolator(selfExe, workspaceRoot))
			app.Logger.Info("os-level shell isolation active",
				"workspace", app.Config.Storage.Path)
		default:
			app.Logger.Warn("shell isolation skipped: cannot resolve self executable",
				"error", exeErr)
		}
	} else {
		app.Logger.Debug("os isolation unavailable on this platform; permissions model only",
			"reason", isoCap.Reason)
	}
	toolsReg.SetRequireShellIsolation(app.Config.Permissions.Shell.RequireIsolation)

	// Create daemon
	d, err := daemon.New(app.Config, st, llmReg, toolsReg, permsEng, app.Logger, addr, v1Deps)
	if err != nil {
		return fmt.Errorf("create daemon: %w", err)
	}

	// Handle shutdown signals
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	return d.Start(ctx)
}

func runAttach(ctx context.Context, sessionID string) error {
	// Read daemon address
	addr, err := readDaemonAddr()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	// Connect to WebSocket
	url := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "client disconnect")

	fmt.Printf("Attached to session %s (daemon at %s)\n", sessionID, addr)
	fmt.Println("Type messages to send, Ctrl+C to detach")

	// Subscribe to session events
	subNotif, _ := daemon.NewNotification(daemon.MethodSessionEvent, map[string]any{
		"action":     "subscribe",
		"session_id": sessionID,
	})
	subData, _ := json.Marshal(subNotif)
	conn.Write(ctx, websocket.MessageText, subData)

	// Read loop
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			fmt.Printf("\r%s\n> ", string(data))
		}
	}()

	// Write loop
	for {
		var input string
		fmt.Print("> ")
		_, err := fmt.Scanln(&input)
		if err != nil {
			break
		}
		if input == "" {
			continue
		}

		req := daemon.JSONRPCRequest{
			JSONRPC: "2.0",
			Method:  daemon.MethodExecuteTurn,
			Params:  json.RawMessage(fmt.Sprintf(`{"session_id":"%s","user_message":%s}`, sessionID, jsonMarshal(input))),
		}
		reqData, _ := json.Marshal(req)
		conn.Write(ctx, websocket.MessageText, reqData)
	}

	return nil
}

func runHalt(ctx context.Context, args []string) error {
	addr, err := readDaemonAddr()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	url := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "client disconnect")

	var req daemon.JSONRPCRequest
	if len(args) == 0 {
		// Global halt
		req = daemon.JSONRPCRequest{
			JSONRPC: "2.0",
			Method:  daemon.MethodHaltAll,
		}
	} else {
		// Per-session halt
		req = daemon.JSONRPCRequest{
			JSONRPC: "2.0",
			Method:  daemon.MethodHaltSession,
			Params:  json.RawMessage(fmt.Sprintf(`{"session_id":"%s","reason":"user"}`, args[0])),
		}
	}

	reqData, _ := json.Marshal(req)
	conn.Write(ctx, websocket.MessageText, reqData)

	// Read response
	_, data, _ := conn.Read(ctx)
	fmt.Println(string(data))
	return nil
}

func runResume(ctx context.Context, sessionID string) error {
	addr, err := readDaemonAddr()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	url := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "client disconnect")

	req := daemon.JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  daemon.MethodResumeSession,
		Params:  json.RawMessage(fmt.Sprintf(`{"session_id":"%s"}`, sessionID)),
	}

	reqData, _ := json.Marshal(req)
	conn.Write(ctx, websocket.MessageText, reqData)

	_, data, _ := conn.Read(ctx)
	fmt.Println(string(data))
	return nil
}

func runSessions(ctx context.Context) error {
	addr, err := readDaemonAddr()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	url := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "client disconnect")

	req := daemon.JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  daemon.MethodListSessions,
		Params:  json.RawMessage(`{"limit":50}`),
	}

	reqData, _ := json.Marshal(req)
	conn.Write(ctx, websocket.MessageText, reqData)

	_, data, _ := conn.Read(ctx)
	fmt.Println(string(data))
	return nil
}

func runStatus(ctx context.Context) error {
	addr, err := readDaemonAddr()
	if err != nil {
		fmt.Println("daemon: not running")
		return nil
	}

	url := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		fmt.Printf("daemon: unreachable (%v)\n", err)
		return nil
	}
	defer conn.Close(websocket.StatusNormalClosure, "client disconnect")

	req := daemon.JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  daemon.MethodStatus,
	}

	reqData, _ := json.Marshal(req)
	conn.Write(ctx, websocket.MessageText, reqData)

	_, data, _ := conn.Read(ctx)
	fmt.Println(string(data))
	return nil
}

func readDaemonAddr() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	addrFile := filepath.Join(home, ".forge", "daemon.addr")
	data, err := os.ReadFile(addrFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func jsonMarshal(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
