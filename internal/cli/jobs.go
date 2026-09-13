package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newJobsCommand())
	RootCommand.AddCommand(newJobCommand())
}

func newJobsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Background jobs queue (RF-1.4)",
	}
	cmd.AddCommand(newJobsListCommand())
	return cmd
}

func newJobCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Job operations: follow, cancel",
	}
	cmd.AddCommand(newJobFollowCommand())
	cmd.AddCommand(newJobCancelCommand())
	return cmd
}

func newJobsListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List background jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobsList(cmd.Context(), cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func newJobFollowCommand() *cobra.Command {
	var jsonOut bool
	var noWait bool
	cmd := &cobra.Command{
		Use:   "follow <job-id>",
		Short: "Follow a job (re-attach to detached turn)",
		Long:  "Polls the job and its session transcript via GetMessagesSince. Reuses the reconnect contract: follow survives daemon restarts via daemon.addr.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobFollow(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut, !noWait)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "do not wait for running job to finish; print current state only")
	return cmd
}

func newJobCancelCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobCancel(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	return cmd
}

func runJobsList(ctx context.Context, out io.Writer, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "jobs list", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.ListJobs(ctx)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "jobs list", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "jobs list", res)
	}
	if len(res.Jobs) == 0 {
		fmt.Fprintln(os.Stdout, "No jobs")
		return nil
	}
	fmt.Fprintf(os.Stdout, "%-45s %-10s %-12s %-20s %s\n", "JOB ID", "STATUS", "SESSION", "CREATED", "ERROR")
	for _, j := range res.Jobs {
		sessShort := j.SessionID
		if len(sessShort) > 8 {
			sessShort = sessShort[:8]
		}
		errStr := j.Error
		if len(errStr) > 30 {
			errStr = errStr[:30] + "..."
		}
		created := "-"
		if j.CreatedAt > 0 {
			created = time.UnixMilli(j.CreatedAt).Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(os.Stdout, "%-45s %-10s %-12s %-20s %s\n", j.ID, j.Status, sessShort, created, errStr)
	}
	return nil
}

func runJobFollow(ctx context.Context, out io.Writer, jobID string, jsonOut bool, wait bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "job follow", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()

	job, err := cl.GetJob(ctx, jobID)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "job follow", err.Error())
		}
		return err
	}

	// JSON mode: return job + current transcript slice
	if jsonOut {
		msgs, _ := cl.GetMessagesSince(ctx, job.SessionID, job.StartSeq)
		var mrs []daemon.MessageResult
		if msgs != nil {
			mrs = msgs.Messages
		}
		payload := map[string]any{
			"job":      job,
			"messages": mrs,
		}
		return writeJSONResultEnvelope(out, "job follow", payload)
	}

	fmt.Fprintf(os.Stdout, "Job %s [%s] session %s (start_seq=%d)\n", job.ID, job.Status, job.SessionID, job.StartSeq)

	// Print existing messages since job start
	lastSeq := job.StartSeq
	printMessages := func(since int) int {
		res, err := cl.GetMessagesSince(ctx, job.SessionID, since)
		if err != nil {
			return since
		}
		maxSeq := since
		for _, m := range res.Messages {
			fmt.Fprintf(os.Stdout, "[%d] %s: %s\n", m.Seq, m.Role, truncate(m.Content, 500))
			if m.Seq > maxSeq {
				maxSeq = m.Seq
			}
		}
		return maxSeq
	}
	lastSeq = printMessages(lastSeq)

	if !wait {
		return nil
	}

	// Poll until job leaves running state
	for {
		updated, err := cl.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		job = updated
		if job.Status != daemon.JobRunning {
			fmt.Fprintf(os.Stdout, "Job %s finished with status %s\n", job.ID, job.Status)
			if job.Error != "" {
				fmt.Fprintf(os.Stdout, "Error: %s\n", job.Error)
			}
			// Final drain
			lastSeq = printMessages(lastSeq)
			break
		}
		// Poll messages while running
		newSeq := printMessages(lastSeq)
		if newSeq != lastSeq {
			lastSeq = newSeq
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}

func runJobCancel(ctx context.Context, out io.Writer, jobID string, jsonOut bool) error {
	if out == nil {
		out = os.Stdout
	}
	cl, err := client.Connect(ctx, "")
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "job cancel", err.Error())
		}
		return daemonHint(err)
	}
	defer cl.Close()
	res, err := cl.CancelJob(ctx, jobID)
	if err != nil {
		if jsonOut {
			_ = writeJSONErrorEnvelope(out, "job cancel", err.Error())
		}
		return err
	}
	if jsonOut {
		return writeJSONResultEnvelope(out, "job cancel", res)
	}
	fmt.Fprintf(os.Stdout, "Job %s %s (canceled=%v)\n", res.JobID, res.Status, res.Canceled)
	return nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
