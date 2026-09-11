package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// persistState writes the current RunState to StateDir/.forge/runs/<run_id>/state.json
// for reanudability (RF-11.8). When StateDir is empty, persistence is a no-op.
func (r *Runner) persistState() error {
	if r.StateDir == "" {
		return nil
	}
	dir := filepath.Join(r.StateDir, ".forge", "runs", r.Manifest.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "state.json")
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

func (r *Runner) persistReport(rep *Report) error {
	if r.StateDir == "" {
		return nil
	}
	dir := filepath.Join(r.StateDir, ".forge", "runs", r.Manifest.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	path := filepath.Join(dir, "report.json")
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write report tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename report: %w", err)
	}
	return nil
}

// LoadState reads a persisted RunState for runID under stateDir.
func LoadState(stateDir, runID string) (*RunState, error) {
	path := filepath.Join(stateDir, ".forge", "runs", runID, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load state %s: %w", path, err)
	}
	var s RunState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return &s, nil
}

// LoadReport reads a persisted Report for runID under stateDir.
func LoadReport(stateDir, runID string) (*Report, error) {
	path := filepath.Join(stateDir, ".forge", "runs", runID, "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load report %s: %w", path, err)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("decode report: %w", err)
	}
	return &rep, nil
}
