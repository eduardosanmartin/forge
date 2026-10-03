package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// persistState writes the current RunState to StateDir/.forge/runs/<run_id>/state.json
// for reanudability (RF-11.8). When StateDir is empty, persistence is a no-op.
// When r.auditLog is set (sensitivity regulado/datos-sensibles, RNF-4.10),
// every persisted transition is ALSO appended to the tamper-evident hash
// chain — state.json stays a plain, overwritable current snapshot (that's
// what Resume reads), while audit.jsonl is the append-only historical
// record compliance evidence actually depends on.
func (r *Runner) persistState() error {
	if r.StateDir == "" {
		return nil
	}
	if r.auditLog != nil {
		if err := r.auditLog.Append("state_change", auditDetailFromState(r.state)); err != nil {
			return fmt.Errorf("append audit record: %w", err)
		}
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

// persistDecomposedTasks writes an LLM-proposed task list to
// StateDir/.forge/runs/<run_id>/tasks.decomposed.json — an audit trail of
// what the Decomposer actually proposed (and what the after_spec_decomposition
// checkpoint approved), independent of state.json's current-snapshot role.
func persistDecomposedTasks(stateDir, runID string, tasks []Task) error {
	dir := filepath.Join(stateDir, ".forge", "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "tasks.decomposed.json")
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal decomposed tasks: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
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

// persistManifest writes the run's manifest (with its task list, decomposed
// or authored) to StateDir/.forge/runs/<run_id>/manifest.json, so a daemon
// that restarts can resume the run from its ID alone (RF-11.8) instead of
// needing a client to resend the manifest.
func (r *Runner) persistManifest() error {
	if r.StateDir == "" {
		return nil
	}
	dir := filepath.Join(r.StateDir, ".forge", "runs", r.Manifest.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(r.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o644)
}

// LoadManifest reads the manifest persisted for runID under stateDir.
func LoadManifest(stateDir, runID string) (*Manifest, error) {
	path := filepath.Join(stateDir, ".forge", "runs", runID, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

// ListStates returns the persisted state of every run under stateDir
// (.forge/runs/*/state.json), skipping unreadable entries. Used by the
// daemon at startup to rediscover runs a restart interrupted.
func ListStates(stateDir string) ([]*RunState, error) {
	entries, err := os.ReadDir(filepath.Join(stateDir, ".forge", "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*RunState
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := LoadState(stateDir, e.Name())
		if err != nil || st.RunID == "" {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// MarkCanceled records in runID's persisted state that a human canceled
// it (status failed, with the reason), so a later daemon restart doesn't
// rediscover it as interrupted again.
func MarkCanceled(stateDir, runID, reason string) error {
	st, err := LoadState(stateDir, runID)
	if err != nil {
		return err
	}
	st.Status = StatusFailed
	st.Error = reason
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, ".forge", "runs", runID, "state.json"), append(data, '\n'), 0o644)
}
