package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/mcpbridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newMCPCommand())
}

// mcpApprovalsPath is where approved MCP tool-list hashes live.
func mcpApprovalsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".forge", "mcp-approvals.json"), nil
}

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "External MCP servers: list, approve, and serve forge itself over MCP (F6)",
		Long: "forge can use tools of external MCP servers declared under mcp.servers in the\n" +
			"config. Each server must be approved first (its tool names, descriptions and\n" +
			"schemas reach the model, so they are reviewed like any external plugin, RNF-4.6),\n" +
			"and each tool must be allowed (or asked for) in permissions.mcp as \"server/tool\".",
	}
	cmd.AddCommand(newMCPListCommand(), newMCPApproveCommand(), newMCPServeCommand())
	return cmd
}

func loadAppConfig(cmd *cobra.Command) *config.Config {
	if app, ok := AppFromContext(cmd.Context()); ok && app != nil && app.Config != nil {
		return app.Config
	}
	return config.Defaults()
}

func newMCPListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Connect to each configured MCP server and show its tools and approval state",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadAppConfig(cmd)
			path, err := mcpApprovalsPath()
			if err != nil {
				return err
			}
			approvals, err := mcpbridge.LoadApprovals(path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(cfg.MCP.Servers) == 0 {
				fmt.Fprintln(out, "No MCP servers configured (mcp.servers in .forge/config.json).")
				return nil
			}
			names := make([]string, 0, len(cfg.MCP.Servers))
			for n := range cfg.MCP.Servers {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				srv, err := mcpbridge.Connect(cmd.Context(), name, cfg.MCP.Servers[name], cfg.Network.AllowedHosts)
				if err != nil {
					fmt.Fprintf(out, "%s: unavailable — %v\n\n", name, err)
					continue
				}
				state := "NOT APPROVED — review below, then: forge mcp approve " + name
				if approvals.Approved(name, srv.Hash) {
					state = "approved"
				}
				fmt.Fprintf(out, "%s: %d tools, %s (hash %s)\n", name, len(srv.Tools), state, srv.Hash[:12])
				for _, t := range srv.Tools {
					fmt.Fprintf(out, "  - %s/%s: %s\n", name, t.Name, oneLine(t.Description))
				}
				fmt.Fprintln(out)
				srv.Close()
			}
			return nil
		},
	}
}

func newMCPApproveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <server>",
		Short: "Approve a server's CURRENT tool list (re-run after it changes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadAppConfig(cmd)
			name := args[0]
			spec, ok := cfg.MCP.Servers[name]
			if !ok {
				return &UsageError{Err: fmt.Errorf("no MCP server %q in mcp.servers", name)}
			}
			srv, err := mcpbridge.Connect(cmd.Context(), name, spec, cfg.Network.AllowedHosts)
			if err != nil {
				return err
			}
			defer srv.Close()
			path, err := mcpApprovalsPath()
			if err != nil {
				return err
			}
			approvals, err := mcpbridge.LoadApprovals(path)
			if err != nil {
				return err
			}
			if err := approvals.Approve(name, srv.Hash); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Approved %s (%d tools, hash %s). Restart the daemon to load it; expose tools with permissions.mcp.allow / ask (\"%s/<tool>\").\n", name, len(srv.Tools), srv.Hash[:12], name)
			return nil
		},
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// --- forge as an MCP server -------------------------------------------------

type forgeTaskIn struct {
	Prompt string `json:"prompt" jsonschema:"the task for forge's agent, in plain language"`
}

type forgeTaskOut struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
}

type forgeRunIn struct {
	Manifest string `json:"manifest" jsonschema:"run manifest JSON (see forge run --manifest)"`
}

type forgeRunStatusIn struct {
	RunID string `json:"run_id" jsonschema:"the run to inspect"`
}

type forgeSessionsOut struct {
	Sessions []string `json:"sessions"`
}

func newMCPServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Expose forge to other agents as an MCP server over stdio",
		Long: "Tools: forge_task (one agent turn in a new session), forge_run_manifest (start an\n" +
			"autonomous run), forge_run_status, forge_sessions. Everything goes through the\n" +
			"running daemon, so its permission policy applies. Checkpoint approval is\n" +
			"deliberately NOT exposed: a human approves checkpoints (RF-11.7).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, err := client.Connect(ctx, "")
			if err != nil {
				return daemonHint(err)
			}
			defer cl.Close()

			s := mcp.NewServer(&mcp.Implementation{Name: "forge", Version: "1"}, nil)
			mcp.AddTool(s, &mcp.Tool{Name: "forge_task", Description: "Run one forge agent turn on the workspace forge is serving, in a new session."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in forgeTaskIn) (*mcp.CallToolResult, forgeTaskOut, error) {
					var sess daemon.SessionResult
					if err := cl.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: map[string]any{"source": "mcp"}}, &sess); err != nil {
						return nil, forgeTaskOut{}, err
					}
					var res daemon.ExecuteTurnResult
					if err := cl.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: sess.ID, UserMessage: in.Prompt}, &res); err != nil {
						return nil, forgeTaskOut{}, err
					}
					return nil, forgeTaskOut{SessionID: sess.ID, Response: res.FinalContent}, nil
				})
			mcp.AddTool(s, &mcp.Tool{Name: "forge_run_manifest", Description: "Start an autonomous forge run from a manifest (checkpoints are approved by a human in forge's TUI/GUI)."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in forgeRunIn) (*mcp.CallToolResult, daemon.RunResult, error) {
					var params daemon.RunStartParams
					if err := json.Unmarshal([]byte(in.Manifest), &params.Manifest); err != nil {
						return nil, daemon.RunResult{}, fmt.Errorf("manifest: %w", err)
					}
					var res daemon.RunResult
					err := cl.Call(ctx, daemon.MethodRunStart, params, &res)
					return nil, res, err
				})
			mcp.AddTool(s, &mcp.Tool{Name: "forge_run_status", Description: "Status of a forge run."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in forgeRunStatusIn) (*mcp.CallToolResult, daemon.RunResult, error) {
					var res daemon.RunResult
					err := cl.Call(ctx, daemon.MethodRunStatus, daemon.RunStatusParams{RunID: in.RunID}, &res)
					return nil, res, err
				})
			mcp.AddTool(s, &mcp.Tool{Name: "forge_sessions", Description: "IDs of forge's sessions, newest first."},
				func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, forgeSessionsOut, error) {
					var res daemon.ListSessionsResult
					if err := cl.Call(ctx, daemon.MethodListSessions, map[string]any{"limit": 50}, &res); err != nil {
						return nil, forgeSessionsOut{}, err
					}
					out := forgeSessionsOut{}
					for _, s := range res.Sessions {
						out.Sessions = append(out.Sessions, s.ID)
					}
					return nil, out, nil
				})
			return s.Run(ctx, &mcp.StdioTransport{})
		},
	}
}
