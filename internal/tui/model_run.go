// Code moved verbatim from model.go (M12 split) — same package, no behavior change.

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// Run panel messages (Fase 4). runStatusTickMsg carries the run_id it was
// scheduled for so a stale in-flight tick from a previously tracked run
// (e.g. the user started a second /run before the first tick fired) is
// dropped instead of clobbering the newer run's state — same guard shape
// as deltaPending/scheduleDeltaRebuild uses for transcript rebuilds.
type runStartMsg struct {
	res *daemon.RunResult
	err error
}

type runStatusMsg struct {
	res *daemon.RunResult
	err error
}

type runApproveMsg struct {
	res *daemon.RunResult
	err error
}

type runCancelMsg struct {
	res *daemon.RunResult
	err error
}

type runStatusTickMsg struct{ runID string }

// cmdRunStart parses manifestPath locally (run.ParseFile — same loader the
// CLI's `forge run` uses, resolving spec_ref and validating before anything
// goes over the wire) and sends the parsed manifest as content to run.start,
// per the Fase 0 flag-mapping decision documented on RunStartParams: the
// daemon never reads the client's filesystem.
func (m Model) cmdRunStart(manifestPath string) tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return runStartMsg{err: fmt.Errorf("not connected")} }
	}
	mani, err := run.ParseFile(manifestPath)
	if err != nil {
		return func() tea.Msg { return runStartMsg{err: err} }
	}
	client := m.client
	return func() tea.Msg {
		res, err := client.RunStart(*mani, "", false)
		return runStartMsg{res: res, err: err}
	}
}

func (m Model) cmdRunStatus(runID string) tea.Cmd {
	if m.client == nil || runID == "" {
		return nil
	}
	client := m.client
	return func() tea.Msg {
		res, err := client.RunStatus(runID)
		return runStatusMsg{res: res, err: err}
	}
}

func (m Model) cmdRunApproveCheckpoint(runID string, approved bool) tea.Cmd {
	if m.client == nil || runID == "" {
		return nil
	}
	client := m.client
	return func() tea.Msg {
		res, err := client.RunApproveCheckpoint(runID, approved)
		return runApproveMsg{res: res, err: err}
	}
}

func (m Model) cmdRunCancel(runID string) tea.Cmd {
	if m.client == nil || runID == "" {
		return nil
	}
	client := m.client
	return func() tea.Msg {
		res, err := client.RunCancel(runID)
		return runCancelMsg{res: res, err: err}
	}
}

// scheduleRunStatusPoll arms a single 1s status-poll tick for runID, guarded
// by runPollPending so overlapping ticks never stack (mirrors
// scheduleDeltaRebuild's deltaPending guard). Call this whenever runResult
// enters or stays in a state that can still change on its own — i.e.
// RunRunning; a paused-at-checkpoint or terminal run has nothing to poll
// for until a local action (approve/decline/cancel) changes it.
func (m *Model) scheduleRunStatusPoll(runID string) tea.Cmd {
	if m.runPollPending || runID == "" {
		return nil
	}
	m.runPollPending = true
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return runStatusTickMsg{runID: runID}
	})
}

// resetRunPanelForSessionSwitch clears the run panel's tracking state on
// every session switch (new session or selecting an existing one) — a run
// belongs to whichever session started it (or was told about via an
// incoming checkpoint event), not to the TUI process globally. Without
// this, ctrl+4 in a session that never ran anything showed a STALE run
// left over from a previous session instead of "no active run" — a real
// bug found live via the QA harness (hojaDeRuta-qa-autonomo-tui.md Fase 4,
// scenario B5). Called from every site that already resets
// entries/lastSeq/pendingUserText for the same reason.
func (m *Model) resetRunPanelForSessionSwitch() {
	m.runID = ""
	m.runResult = nil
	m.runErr = ""
	m.runPanelVisible = false
	m.runPollPending = false
}

// renderRunPanel renders the Fase 4 run-observation panel: current status,
// progress counters, and — when the run is genuinely blocked live in
// OnCheckpoint (see internal/daemon/runs.go's newDaemonManifestRunner) — the
// pending checkpoint with the y/n approve/decline hint. Values come from
// m.runResult, refreshed by /run, the poll tick, or an incoming
// run.checkpoint.event; a nil m.runResult (start RPC in flight, or a
// checkpoint event arrived before the first status fetch landed) renders a
// "loading" line instead of guessing.
func (m Model) renderRunPanel() string {
	var sb strings.Builder
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true)
	text := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim))
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint))
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Warning)).Bold(true)

	sb.WriteString(accent.Render("Run: "+m.runID) + "\n")
	if m.runErr != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Error)).Render("error: "+m.runErr) + "\n")
	}
	if m.runResult == nil {
		sb.WriteString(faint.Render("(loading status…)") + "\n")
		sb.WriteString(dim.Render("esc to close") + "\n")
		return sb.String()
	}
	res := m.runResult
	sb.WriteString(dim.Render("status: ") + text.Render(res.Status) + "\n")
	if res.CurrentTask != "" {
		sb.WriteString(dim.Render("current task: ") + text.Render(res.CurrentTask) + "\n")
	}
	sb.WriteString(dim.Render(fmt.Sprintf("tokens: %s · iterations: %d", formatTokens(res.TokensUsed), res.IterUsed)) + "\n")
	if res.PendingCheckpoint != nil {
		cp := res.PendingCheckpoint
		sb.WriteString(warn.Render(fmt.Sprintf("⚠ checkpoint pending: %s (%s)", cp.ID, cp.Trigger)) + "\n")
		sb.WriteString(text.Render("  y approve · n decline") + "\n")
	}
	if res.Report != nil {
		sb.WriteString(dim.Render(fmt.Sprintf("tasks completed: %d/%d", len(res.Report.CompletedTasks), res.Report.TotalTasks)) + "\n")
		sb.WriteString(dim.Render("validation: ") + text.Render(res.Report.ValidationState) + "\n")
	}
	if res.Error != "" {
		sb.WriteString(faint.Render(truncateError(res.Error, 80)) + "\n")
	}
	sb.WriteString(dim.Render("c cancel · esc close") + "\n")
	return sb.String()
}
