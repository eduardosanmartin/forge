package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newSubagentsCommand())
}

func newSubagentsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subagents",
		Short: "Subagent topology (RF-1.2/1.3)",
		Long:  "Inspect subagent hierarchy: branched child sessions spawned via spawn_subagent. Shows parent/children, depth, task and branch lineage.",
	}
	cmd.AddCommand(newSubagentsListCommand())
	return cmd
}

func newSubagentsListCommand() *cobra.Command {
	var jsonOut bool
	var parentFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subagent sessions with topology (parent, depth, task)",
		Long:  "Lists sessions where metadata subagent=true, with parent/children topology visible. Supports --json for machine-readable output. Filter with --parent <session-id> to show only children of that parent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubagentsList(cmd.Context(), cmd.OutOrStdout(), jsonOut, parentFilter)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&parentFilter, "parent", "", "filter by parent session id")
	return cmd
}

type subagentEntry struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id"`
	RootID     string `json:"root_id,omitempty"`
	Depth      int    `json:"depth"`
	Task       string `json:"task"`
	BranchRoot string `json:"branch_root,omitempty"`
	Model      string `json:"model,omitempty"`
	Messages   int    `json:"message_count"`
	CreatedAt  int64  `json:"created_at"`
}

type subagentsListResult struct {
	Subagents []subagentEntry `json:"subagents"`
	Count     int             `json:"count"`
	Parents   map[string][]string `json:"parents,omitempty"` // parent -> children ids
}

func runSubagentsList(ctx context.Context, out io.Writer, jsonOut bool, parentFilter string) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "subagents list", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.ListSessions(ctx, 200, 0)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "subagents list", err.Error())
		}
		return err
	}
	var entries []subagentEntry
	parents := map[string][]string{}
	for _, s := range res.Sessions {
		if s.Metadata == nil {
			continue
		}
		isSub, _ := s.Metadata["subagent"].(bool)
		if !isSub {
			continue
		}
		parentID, _ := s.Metadata["subagent_parent"].(string)
		if parentFilter != "" && parentID != parentFilter {
			continue
		}
		task, _ := s.Metadata["subagent_task"].(string)
		depth := 0
		switch v := s.Metadata["subagent_depth"].(type) {
		case float64:
			depth = int(v)
		case int:
			depth = v
		case int64:
			depth = int(v)
		}
		root, _ := s.Metadata["branch_root"].(string)
		branchRoot := root
		model, _ := s.Metadata["model"].(string)
		entries = append(entries, subagentEntry{
			ID:         s.ID,
			ParentID:   parentID,
			RootID:     root,
			Depth:      depth,
			Task:       task,
			BranchRoot: branchRoot,
			Model:      model,
			Messages:   s.MessageCount,
			CreatedAt:  s.CreatedAt,
		})
		if parentID != "" {
			parents[parentID] = append(parents[parentID], s.ID)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt < entries[j].CreatedAt })
	result := subagentsListResult{Subagents: entries, Count: len(entries), Parents: parents}
	if jsonOut {
		return writeJSONResultEnvelope(out, "subagents list", result)
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stdout, "No subagents")
		return nil
	}
	fmt.Fprintf(os.Stdout, "%-38s %-8s %-5s %-12s %6s %s\n", "CHILD", "PARENT", "DEPTH", "BRANCH_ROOT", "MSGS", "TASK")
	for _, e := range entries {
		parent := e.ParentID
		if len(parent) > 8 {
			parent = parent[:8]
		}
		if parent == "" {
			parent = "-"
		}
		branch := e.BranchRoot
		if len(branch) > 8 {
			branch = branch[:8]
		}
		if branch == "" {
			branch = "-"
		}
		task := e.Task
		if len(task) > 50 {
			task = task[:47] + "..."
		}
		fmt.Fprintf(os.Stdout, "%-38s %-8s %-5d %-12s %6d %s\n", e.ID, parent, e.Depth, branch, e.Messages, task)
	}
	if len(parents) > 0 {
		fmt.Fprintln(os.Stdout, "\nTopology (parent -> children):")
		keys := make([]string, 0, len(parents))
		for k := range parents {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, p := range keys {
			short := p
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(os.Stdout, "  %s -> %d children\n", short, len(parents[p]))
		}
	}
	return nil
}
