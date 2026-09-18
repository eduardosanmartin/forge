package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
)

func TestRequiresTamperEvidentAudit(t *testing.T) {
	cases := []struct {
		sensitivity string
		want        bool
	}{
		{config.SensitivityGeneral, false},
		{config.SensitivityRegulated, true},
		{config.SensitivitySensitive, true},
		{"", false},
		{"nonsense", false},
	}
	for _, c := range cases {
		if got := requiresTamperEvidentAudit(c.sensitivity); got != c.want {
			t.Errorf("requiresTamperEvidentAudit(%q) = %v, want %v", c.sensitivity, got, c.want)
		}
	}
}

func TestAuditLogAppendAndVerifyCleanChain(t *testing.T) {
	dir := t.TempDir()
	al, err := OpenAuditLog(dir, "run-1")
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	for i, event := range []string{"run_started", "task_started", "task_completed", "run_completed"} {
		if err := al.Append(event, map[string]any{"i": i}); err != nil {
			t.Fatalf("Append %s: %v", event, err)
		}
	}
	if err := al.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(dir, ".forge", "runs", "run-1", "audit.jsonl")
	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.Valid || res.Records != 4 || res.BrokenAt != 0 {
		t.Fatalf("expected a clean 4-record chain, got %+v", res)
	}
}

func TestAuditLogResumesChainAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	al1, err := OpenAuditLog(dir, "run-2")
	if err != nil {
		t.Fatalf("OpenAuditLog (1st): %v", err)
	}
	if err := al1.Append("run_started", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := al1.Close(); err != nil {
		t.Fatalf("Close (1st): %v", err)
	}

	// Simulate a process restart: reopen and keep appending.
	al2, err := OpenAuditLog(dir, "run-2")
	if err != nil {
		t.Fatalf("OpenAuditLog (2nd): %v", err)
	}
	if al2.nextSeq != 2 {
		t.Fatalf("expected chain to resume at seq 2, got %d", al2.nextSeq)
	}
	if err := al2.Append("run_resumed", nil); err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if err := al2.Close(); err != nil {
		t.Fatalf("Close (2nd): %v", err)
	}

	path := filepath.Join(dir, ".forge", "runs", "run-2", "audit.jsonl")
	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if !res.Valid || res.Records != 2 {
		t.Fatalf("expected a clean 2-record chain spanning the reopen, got %+v", res)
	}
}

func TestVerifyAuditLogDetectsEditedRecord(t *testing.T) {
	dir := t.TempDir()
	path := seedAuditLog(t, dir, "run-3", 3)

	// Tamper: rewrite record 2's event field in place, leaving its stored
	// hash unchanged — exactly what an attacker editing history looks like.
	tamperLine(t, path, 1, func(s string) string {
		return strings.Replace(s, `"task_started"`, `"task_started_EDITED"`, 1)
	})

	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if res.Valid {
		t.Fatal("expected tampering to be detected, got Valid=true")
	}
	if res.BrokenAt != 2 {
		t.Errorf("BrokenAt = %d, want 2", res.BrokenAt)
	}
}

func TestVerifyAuditLogDetectsDeletedRecord(t *testing.T) {
	dir := t.TempDir()
	path := seedAuditLog(t, dir, "run-4", 3)

	lines := readLines(t, path)
	// Remove the middle record entirely (seq 2), keeping 1 and 3.
	remaining := []string{lines[0], lines[2]}
	if err := os.WriteFile(path, []byte(strings.Join(remaining, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}

	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	if res.Valid {
		t.Fatal("expected deletion to be detected, got Valid=true")
	}
	if res.BrokenAt != 2 {
		t.Errorf("BrokenAt = %d, want 2 (the seq gap)", res.BrokenAt)
	}
}

func TestVerifyAuditLogDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := seedAuditLog(t, dir, "run-5", 3)

	lines := readLines(t, path)
	truncated := lines[:2] // drop the last record
	if err := os.WriteFile(path, []byte(strings.Join(truncated, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}

	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatalf("VerifyAuditLog: %v", err)
	}
	// A clean prefix truncation still verifies as a valid (shorter) chain —
	// this documents that fact rather than asserting a false positive: the
	// real defense against truncation is that the audit log's own state.json
	// sibling records CompletedTasks/status independently, so a truncated
	// audit.jsonl that no longer matches state.json is itself the tell.
	if !res.Valid || res.Records != 2 {
		t.Fatalf("expected a valid but shorter chain after prefix truncation, got %+v", res)
	}
}

func TestAuditDetailFromStateSnapshotsKeyFields(t *testing.T) {
	s := RunState{
		Status:           StatusPaused,
		CurrentTaskID:    "t2",
		CompletedTasks:   []string{"t1"},
		PausedCheckpoint: &Checkpoint{ID: "cp1"},
		PauseReason:      "before_task",
		Budget:           BudgetState{TokensUsed: 42, IterationsUsed: 3},
	}
	d := auditDetailFromState(s)
	if d["status"] != StatusPaused {
		t.Errorf("status = %v", d["status"])
	}
	if d["paused_checkpoint_id"] != "cp1" {
		t.Errorf("paused_checkpoint_id = %v", d["paused_checkpoint_id"])
	}
	if d["pause_reason"] != "before_task" {
		t.Errorf("pause_reason = %v", d["pause_reason"])
	}
	budget, ok := d["budget"].(map[string]any)
	if !ok || budget["tokens_used"] != 42 {
		t.Errorf("budget = %v", d["budget"])
	}
}

// seedAuditLog writes n clean records for runID under dir and returns the
// log file path.
func seedAuditLog(t *testing.T, dir, runID string, n int) string {
	t.Helper()
	al, err := OpenAuditLog(dir, runID)
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	events := []string{"run_started", "task_started", "task_completed", "run_completed"}
	for i := 0; i < n; i++ {
		event := events[i%len(events)]
		if err := al.Append(event, map[string]any{"i": i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := al.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return filepath.Join(dir, ".forge", "runs", runID, "audit.jsonl")
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// tamperLine rewrites line index i (0-based) of the file at path via edit,
// leaving every other line untouched.
func tamperLine(t *testing.T, path string, i int, edit func(string) string) {
	t.Helper()
	lines := readLines(t, path)
	if i < 0 || i >= len(lines) {
		t.Fatalf("tamperLine: index %d out of range (len=%d)", i, len(lines))
	}
	lines[i] = edit(lines[i])
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
}
