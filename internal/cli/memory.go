package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newMemoryCommand())
}

// defaultAnchorSession mirrors the daemon memory.create convention: CLI-only
// anchors created without --session land in the cross-session "global"
// bucket so they still show up in `forge memory list` and survive session
// deletions.
const defaultAnchorSession = "global"

func newMemoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and edit anchored facts (RF-3.4)",
		Long:  "Every forge memory subcommand supports --json (one envelope per invocation). Anchors created without --session use the cross-session \"global\" bucket. These commands are owner-initiated inspection/editing of anchored facts and deliberately bypass model-facing permission gates.",
	}
	cmd.AddCommand(newMemoryListCommand())
	cmd.AddCommand(newMemoryGetCommand())
	cmd.AddCommand(newMemoryAddCommand())
	cmd.AddCommand(newMemoryEditCommand())
	cmd.AddCommand(newMemoryDeleteCommand())
	return cmd
}

func newMemoryListCommand() *cobra.Command {
	var jsonOut bool
	var session string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List anchored facts (all sessions, or filtered by --session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryList(cmd.Context(), cmd.OutOrStdout(), session, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&session, "session", "", "filter anchors by session id")
	return cmd
}

func newMemoryGetCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show one anchored fact in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryGet(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func newMemoryAddCommand() *cobra.Command {
	var jsonOut bool
	var session string
	var source string
	var tags []string
	cmd := &cobra.Command{
		Use:   "add <content>",
		Short: "Anchor a persistent fact (defaults: --session \"global\", --source \"user\")",
		Long:  "Anchors persist across sessions and restarts. Without --session the anchor goes to the cross-session \"global\" bucket. --tags takes a comma-separated list.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := strings.Join(args, " ")
			return runMemoryAdd(cmd.Context(), cmd.OutOrStdout(), content, session, source, tags, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&session, "session", "", "anchor source session id (default \"global\")")
	cmd.Flags().StringVar(&source, "source", "user", "anchor source label (user|assistant|auto)")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "comma-separated tags")
	return cmd
}

func newMemoryEditCommand() *cobra.Command {
	var jsonOut bool
	var content string
	var source string
	var tags []string
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit an anchor's content, source, or tags",
		Long:  "Only the flags you pass are changed. --tags replaces the full tag list (pass --tags=\"\" to clear).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryEdit(cmd.Context(), cmd.OutOrStdout(), args[0], content, source, tags, jsonOut, cmd)
		},
	}
	cmd.Flags().StringVar(&content, "content", "", "new content (omitted = unchanged)")
	cmd.Flags().StringVar(&source, "source", "", "new source label (omitted = unchanged)")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "comma-separated tags replacing the full list (omitted = unchanged)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func newMemoryDeleteCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Remove an anchor",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryDelete(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func parseAnchorID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("anchor id must be a positive integer, got %q", raw)
	}
	return id, nil
}

type memoryDeleteResult struct {
	Deleted bool  `json:"deleted"`
	ID      int64 `json:"id"`
}

func runMemoryList(ctx context.Context, out io.Writer, session string, jsonOut bool) error {
	return memoryCall(ctx, out, "memory list", jsonOut, daemon.MemoryListParams{SessionID: session},
		func(res daemon.MemoryListResult) {
			if len(res.Anchors) == 0 {
				fmt.Fprintln(out, "No anchors")
				return
			}
			fmt.Fprintf(out, "%-4s %-8s %-9s %-17s %s\n", "ID", "SESSION", "SOURCE", "UPDATED", "CONTENT")
			for _, a := range res.Anchors {
				fmt.Fprintf(out, "%-4d %-8s %-9s %-17s %s\n",
					a.ID, shortSession(a.SessionID), a.Source,
					time.UnixMilli(a.UpdatedAt).Format("2006-01-02 15:04"), truncate(a.Content, 80))
			}
		})
}

func runMemoryGet(ctx context.Context, out io.Writer, rawID string, jsonOut bool) error {
	id, err := parseAnchorID(rawID)
	if err != nil {
		return err
	}
	return memoryCall(ctx, out, "memory get", jsonOut, daemon.MemoryGetParams{ID: id},
		func(res daemon.MemoryResult) { printAnchorFull(out, res.Anchor) })
}

func runMemoryAdd(ctx context.Context, out io.Writer, content, session, source string, tags []string, jsonOut bool) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("content is required")
	}
	if session == "" {
		session = defaultAnchorSession
	}
	params := daemon.MemoryCreateParams{Content: content, SessionID: session, Source: source, Tags: tags}
	return memoryCall(ctx, out, "memory add", jsonOut, params, func(res daemon.MemoryResult) {
		fmt.Fprintf(out, "Anchored #%d [%s] in %s\n", res.Anchor.ID, res.Anchor.Source, shortSession(res.Anchor.SessionID))
	})
}

type memoryEditor struct {
	content *string
	source  *string
	tags    *[]string
}

// buildMemoryEditor turns CLI flags into the RPC patch. Only flags the user
// actually passed are set; cobra cmd.Changed distinguishes "not passed"
// from "passed empty" (e.g. --tags="" clears the tag list).
func buildMemoryEditor(content, source string, tags []string, cmd *cobra.Command) memoryEditor {
	e := memoryEditor{}
	if cmd.Flags().Changed("content") {
		e.content = &content
	}
	if cmd.Flags().Changed("source") {
		e.source = &source
	}
	if cmd.Flags().Changed("tags") {
		e.tags = &tags
	}
	return e
}

func runMemoryEdit(ctx context.Context, out io.Writer, rawID, content, source string, tags []string, jsonOut bool, cmd *cobra.Command) error {
	id, err := parseAnchorID(rawID)
	if err != nil {
		return err
	}
	e := buildMemoryEditor(content, source, tags, cmd)
	params := daemon.MemoryUpdateParams{ID: id, Content: e.content, Source: e.source, Tags: e.tags}
	return memoryCall(ctx, out, "memory edit", jsonOut, params, func(res daemon.MemoryResult) {
		fmt.Fprintf(out, "Updated anchor #%d\n", res.Anchor.ID)
		printAnchorFull(out, res.Anchor)
	})
}

func runMemoryDelete(ctx context.Context, out io.Writer, rawID string, jsonOut bool) error {
	id, err := parseAnchorID(rawID)
	if err != nil {
		return err
	}
	return memoryCall(ctx, out, "memory delete", jsonOut, daemon.MemoryDeleteParams{ID: id},
		func(res memoryDeleteResult) {
			fmt.Fprintf(out, "Deleted anchor #%d\n", res.ID)
		})
}

func shortSession(id string) string {
	if id == "" || id == defaultAnchorSession {
		return id
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func printAnchorFull(out io.Writer, a daemon.AnchorResult) {
	fmt.Fprintf(out, "Anchor #%d\n", a.ID)
	fmt.Fprintf(out, "  Session:    %s\n", a.SessionID)
	fmt.Fprintf(out, "  Source:     %s\n", a.Source)
	fmt.Fprintf(out, "  Tags:       %s\n", strings.Join(a.Tags, ", "))
	fmt.Fprintf(out, "  Created:    %s\n", time.UnixMilli(a.CreatedAt).Format("2006-01-02 15:04:05"))
	fmt.Fprintf(out, "  Updated:    %s\n", time.UnixMilli(a.UpdatedAt).Format("2006-01-02 15:04:05"))
	fmt.Fprintf(out, "  Content:    %s\n", a.Content)
}

// memoryMethodFor maps CLI command names to daemon RPC methods.
func memoryMethodFor(command string) (string, bool) {
	switch command {
	case "memory list":
		return daemon.MethodMemoryList, true
	case "memory get":
		return daemon.MethodMemoryGet, true
	case "memory add":
		return daemon.MethodMemoryCreate, true
	case "memory edit":
		return daemon.MethodMemoryUpdate, true
	case "memory delete":
		return daemon.MethodMemoryDelete, true
	}
	return "", false
}

// memoryCall connects, runs one memory.* RPC, and renders either the JSON
// envelope (RF-6.3: exactly one per invocation) or the human renderer.
func memoryCall[T any](ctx context.Context, out io.Writer, command string, jsonOut bool, params any, render func(T)) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, command, err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	method, ok := memoryMethodFor(command)
	if !ok {
		return fmt.Errorf("unknown memory command %q", command)
	}
	var res T
	if err := cl.Call(ctx, method, params, &res); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, command, err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, command, res)
	}
	render(res)
	return nil
}
