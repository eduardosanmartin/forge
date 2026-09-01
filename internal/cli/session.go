package cli

import (
	"context"
	"fmt"
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
		Short: "Session operations (success marker, replay)",
	}
	cmd.AddCommand(newSessionSuccessCommand())
	cmd.AddCommand(newSessionReplayCommand())
	return cmd
}

func newSessionSuccessCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "success <session-id>",
		Short: "Mark a session as human-verified successful (RF-4.4 input gate)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionSuccess(cmd.Context(), args[0])
		},
	}
}

func runSessionSuccess(ctx context.Context, sessionID string) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()
	if err := cl.MarkSuccess(ctx, sessionID); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Session %s marked as successful\n", sessionID)
	return nil
}

func newSessionReplayCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <session-id>",
		Short: "Replay a session's full transcript grouped by turns (RNF-6.2)",
		Long: "Renders the session's persisted messages as grouped turns: user prompts, assistant tool calls (redacted), tool results (redacted, truncated), and token usage totals. The message store IS the recording; this command is the replay presentation.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionReplay(cmd.Context(), args[0])
		},
	}
}

func runSessionReplay(ctx context.Context, sessionID string) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	if _, err := cl.GetSession(ctx, sessionID); err != nil {
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	const fetchLimit = 1000
	res, err := cl.GetMessages(ctx, sessionID, fetchLimit, 0)
	if err != nil {
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

	output := recording.FormatReplay(msgs, logging.Redact)
	fmt.Fprint(os.Stdout, output)
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
