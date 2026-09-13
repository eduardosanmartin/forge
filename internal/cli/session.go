package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/logging"
	"github.com/eduardosanmartin/forge/internal/recording"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newSessionCommand())
}

func newSessionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Session operations (success marker, replay, branching)",
	}
	cmd.AddCommand(newSessionSuccessCommand())
	cmd.AddCommand(newSessionReplayCommand())
	cmd.AddCommand(newSessionBranchCommand())
	cmd.AddCommand(newSessionMergeCommand())
	cmd.AddCommand(newSessionListCommand())
	cmd.AddCommand(newSessionSwitchCommand())
	return cmd
}

func newSessionSuccessCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "success <session-id>",
		Short: "Mark a session as human-verified successful (RF-4.4 input gate)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionSuccess(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionSuccess(ctx context.Context, out io.Writer, sessionID string, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session success", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	if err := cl.MarkSuccess(ctx, sessionID); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session success", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "session success", map[string]any{"session_id": sessionID, "success": true})
	}
	fmt.Fprintf(os.Stdout, "Session %s marked as successful\n", sessionID)
	return nil
}

func newSessionReplayCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "replay <session-id>",
		Short: "Replay a session's full transcript grouped by turns (RNF-6.2)",
		Long: "Renders the session's persisted messages as grouped turns: user prompts, assistant tool calls (redacted), tool results (redacted, truncated), and token usage totals. The message store IS the recording; this command is the replay presentation.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionReplay(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionReplay(ctx context.Context, out io.Writer, sessionID string, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session replay", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()

	if _, err := cl.GetSession(ctx, sessionID); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session replay", err.Error())
		}
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	const fetchLimit = 1000
	res, err := cl.GetMessages(ctx, sessionID, fetchLimit, 0)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session replay", err.Error())
		}
		return fmt.Errorf("get messages: %w", err)
	}
	msgs := daemonMessagesToStore(res.Messages)
	if len(msgs) == fetchLimit {
		offset := fetchLimit
		for {
			res2, err := cl.GetMessages(ctx, sessionID, fetchLimit, offset)
			if err != nil {
				break
			}
			if len(res2.Messages) == 0 {
				break
			}
			extra := daemonMessagesToStore(res2.Messages)
			msgs = append(msgs, extra...)
			if len(res2.Messages) < fetchLimit {
				break
			}
			offset += fetchLimit
		}
	}

	if jsonOut {
		// Emit raw message results as JSON plus a human replay text field, all
		// inside the standard envelope.
		type replayResult struct {
			SessionID string                `json:"session_id"`
			Messages  []daemon.MessageResult `json:"messages"`
			Replay    string                `json:"replay"`
		}
		rr := replayResult{
			SessionID: sessionID,
			Messages:  res.Messages,
			Replay:    recording.FormatReplay(msgs, logging.Redact),
		}
		// Append paginated extras if any
		if len(msgs) != len(res.Messages) {
			// res.Messages already contains first page; for JSON we return combined via helper above
			// Re-collect via pagination already merged into msgs, but daemon raw not needed separately.
		}
		return writeJSONResultEnvelope(out, "session replay", rr)
	}

	output := recording.FormatReplay(msgs, logging.Redact)
	fmt.Fprint(os.Stdout, output)
	return nil
}

func newSessionBranchCommand() *cobra.Command {
	var atSeq int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "branch <source-session-id>",
		Short: "Branch a session (RF-9.1)",
		Long:  "Creates a new session branched from the source session. When --at is given only messages up to that seq are copied; otherwise the full transcript is copied. Branch lineage is recorded in metadata (branch_parent/root/at_seq).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionBranch(cmd.Context(), cmd.OutOrStdout(), args[0], atSeq, jsonOut)
		},
	}
	cmd.Flags().IntVar(&atSeq, "at", 0, "branch point seq (0 = full copy)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionBranch(ctx context.Context, out io.Writer, sourceID string, atSeq int, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session branch", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.BranchSession(ctx, sourceID, atSeq, nil)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session branch", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "session branch", res)
	}
	fmt.Fprintf(os.Stdout, "Branched %s -> %s (at_seq=%d, msgs=%d)\n", sourceID, res.ID, atSeq, res.MessageCount)
	return nil
}

func newSessionMergeCommand() *cobra.Command {
	var into string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "merge <source-session-id> --into <target-session-id>",
		Short: "Merge a branch tail into a target session (append-tail)",
		Long:  "Appends the source session's tail (messages after branch_at_seq) onto the target session and records merge metadata (merged_from). No 3-way conflict resolution — append-tail only.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if into == "" {
				return fmt.Errorf("flag --into is required")
			}
			return runSessionMerge(cmd.Context(), cmd.OutOrStdout(), args[0], into, jsonOut)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "target session id to merge into (required)")
	_ = cmd.MarkFlagRequired("into")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionMerge(ctx context.Context, out io.Writer, sourceID, targetID string, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session merge", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.MergeSession(ctx, sourceID, targetID)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session merge", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "session merge", res)
	}
	mergedFrom, _ := res.Metadata["merged_from"].(string)
	mergedCount := 0
	switch v := res.Metadata["merged_count"].(type) {
	case float64:
		mergedCount = int(v)
	case int:
		mergedCount = v
	case int64:
		mergedCount = int(v)
	}
	if mergedFrom == "" {
		mergedFrom = sourceID
	}
	fmt.Fprintf(os.Stdout, "Merged %s -> %s (msgs=%d, branch=%s)\n", mergedFrom, res.ID, res.MessageCount, mergedFrom)
	if mergedCount >= 0 {
		fmt.Fprintf(os.Stdout, "Appended %d messages from %s (merged_from=%s)\n", mergedCount, sourceID, mergedFrom)
	}
	return nil
}

func newSessionListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions with branch info",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionList(cmd.Context(), cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionList(ctx context.Context, out io.Writer, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session list", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.ListSessions(ctx, 50, 0)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session list", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "session list", res)
	}
	fmt.Fprintf(os.Stdout, "%-38s %-17s %6s %-12s %s\n", "SESSION", "CREATED", "MSGS", "BRANCH_PARENT", "MODEL")
	for _, s := range res.Sessions {
		parent := "-"
		if p, ok := s.Metadata["branch_parent"].(string); ok && p != "" {
			if len(p) > 8 {
				parent = p[:8]
			} else {
				parent = p
			}
		}
		model := "-"
		if m, ok := s.Metadata["model"].(string); ok && m != "" {
			model = m
		}
		created := "-"
		if s.CreatedAt > 0 {
			// Use local formatting via store timestamp.
			created = fmt.Sprintf("%d", s.CreatedAt)
		}
		fmt.Fprintf(os.Stdout, "%-38s %-17s %6d %-12s %s\n", s.ID, created, s.MessageCount, parent, model)
	}
	return nil
}

func newSessionSwitchCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "switch <session-id>",
		Short: "Switch to a session/branch (validates existence)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionSwitch(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runSessionSwitch(ctx context.Context, out io.Writer, sessionID string, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session switch", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	if _, err := cl.GetSession(ctx, sessionID); err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "session switch", err.Error())
		}
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "session switch", map[string]any{"session_id": sessionID})
	}
	fmt.Fprintf(os.Stdout, "Switched to session %s (use 'forge chat --session %s' or /attach %s in REPL)\n", sessionID, sessionID, sessionID)
	return nil
}

func daemonMessagesToStore(in []daemon.MessageResult) []store.Message {
	out := make([]store.Message, 0, len(in))
	for _, m := range in {
		var toolCalls []llm.ToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, llm.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: llm.ToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		var usage *llm.Usage
		if m.Usage != nil {
			usage = &llm.Usage{
				PromptTokens:     m.Usage.PromptTokens,
				CompletionTokens: m.Usage.CompletionTokens,
				TotalTokens:      m.Usage.TotalTokens,
			}
		}
		out = append(out, store.Message{
			ID:         m.ID,
			Seq:        m.Seq,
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
			Usage:      usage,
			CreatedAt:  m.CreatedAt,
		})
	}
	return out
}
