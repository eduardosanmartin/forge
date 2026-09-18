package run

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
)

// AuditRecord is one hash-chained entry in a run's tamper-evident audit log
// (RNF-4.10). Hash covers every other field of this record plus PrevHash,
// so altering, reordering, or deleting a past record breaks the chain from
// that point forward — detectable by VerifyAuditLog without needing a
// separate signing key or external service.
type AuditRecord struct {
	Seq       int            `json:"seq"`
	Timestamp time.Time      `json:"timestamp"`
	Event     string         `json:"event"`
	Detail    map[string]any `json:"detail,omitempty"`
	PrevHash  string         `json:"prev_hash"`
	Hash      string         `json:"hash"`
}

// auditGenesisHash seeds the chain for a log's first record (record 1's
// PrevHash). A fixed, documented constant rather than an empty string makes
// "chain not yet started" and "chain tampered into an empty prev" visibly
// different failure modes.
const auditGenesisHash = "genesis"

// hashRecord computes the chain hash for a record given its other fields:
// sha256(prevHash || seq || timestamp(RFC3339Nano) || event || canonical detail JSON).
func hashRecord(seq int, ts time.Time, event string, detail map[string]any, prevHash string) (string, error) {
	detailJSON, err := canonicalJSON(detail)
	if err != nil {
		return "", fmt.Errorf("marshal detail: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s", prevHash, seq, ts.UTC().Format(time.RFC3339Nano), event, detailJSON)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalJSON marshals v (a map, in practice) with sorted keys so the same
// logical content always hashes identically. encoding/json already sorts
// map[string]any keys on marshal, so this is a thin, explicitly-named
// wrapper documenting that the hash depends on that behavior.
func canonicalJSON(v map[string]any) (string, error) {
	if v == nil {
		v = map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// requiresTamperEvidentAudit reports whether sensitivity mandates a
// hash-chained audit trail for this run (RNF-4.10: regulado or
// datos-sensibles). Unrecognized/empty values are treated as not requiring
// it — the same fail-safe default other sensitivity checks in this package
// use (see isHighSensitivity/cfgSensitivityRequiresPreMerge).
func requiresTamperEvidentAudit(sensitivity string) bool {
	n, ok := configNormalize(sensitivity)
	if !ok {
		return false
	}
	return n == config.SensitivityRegulated || n == config.SensitivitySensitive
}

// auditDetailFromState snapshots the fields of a RunState worth recording
// per transition — everything persistState already writes to state.json,
// so the audit chain and the resumable snapshot never disagree about what
// happened at a given point.
func auditDetailFromState(s RunState) map[string]any {
	detail := map[string]any{
		"status":          s.Status,
		"current_task_id": s.CurrentTaskID,
		"completed_tasks": append([]string(nil), s.CompletedTasks...),
		"budget": map[string]any{
			"tokens_used":     s.Budget.TokensUsed,
			"iterations_used": s.Budget.IterationsUsed,
		},
	}
	if s.PausedCheckpoint != nil {
		detail["paused_checkpoint_id"] = s.PausedCheckpoint.ID
	}
	if s.PauseReason != "" {
		detail["pause_reason"] = s.PauseReason
	}
	if s.Error != "" {
		detail["error"] = s.Error
	}
	return detail
}

// AuditLog is an append-only, hash-chained log for one run (RNF-4.10),
// active only when the project's sensitivity classification is `regulado`
// or `datos-sensibles` — see requiresTamperEvidentAudit. It opens its file
// O_APPEND so even forge itself cannot rewrite a past line short of
// truncating the file outright, which VerifyAuditLog also treats as
// tampering evidence (the chain simply won't reach the expected tip).
type AuditLog struct {
	path     string
	file     *os.File
	lastHash string
	nextSeq  int
}

// OpenAuditLog opens (creating if needed) the audit log at
// <stateDir>/.forge/runs/<runID>/audit.jsonl, replaying existing records to
// resume the hash chain from where it left off — e.g. across a process
// restart via Resume (RF-11.8).
func OpenAuditLog(stateDir, runID string) (*AuditLog, error) {
	dir := filepath.Join(stateDir, ".forge", "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit log dir: %w", err)
	}
	path := filepath.Join(dir, "audit.jsonl")

	lastHash := auditGenesisHash
	nextSeq := 1
	if existing, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(existing)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var rec AuditRecord
			if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
				existing.Close()
				return nil, fmt.Errorf("audit log %s is corrupt at existing record: %w", path, err)
			}
			lastHash = rec.Hash
			nextSeq = rec.Seq + 1
		}
		scanErr := scanner.Err()
		existing.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("read existing audit log %s: %w", path, scanErr)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s for append: %w", path, err)
	}
	return &AuditLog{path: path, file: f, lastHash: lastHash, nextSeq: nextSeq}, nil
}

// Append writes one more hash-chained record, linked to the previous one.
func (a *AuditLog) Append(event string, detail map[string]any) error {
	ts := time.Now()
	seq := a.nextSeq
	hash, err := hashRecord(seq, ts, event, detail, a.lastHash)
	if err != nil {
		return err
	}
	rec := AuditRecord{Seq: seq, Timestamp: ts, Event: event, Detail: detail, PrevHash: a.lastHash, Hash: hash}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	line = append(line, '\n')
	if _, err := a.file.Write(line); err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	if err := a.file.Sync(); err != nil {
		return fmt.Errorf("sync audit log: %w", err)
	}
	a.lastHash = hash
	a.nextSeq++
	return nil
}

// Close releases the underlying file handle.
func (a *AuditLog) Close() error {
	if a.file == nil {
		return nil
	}
	return a.file.Close()
}

// VerifyResult reports the outcome of checking one audit log's chain.
type VerifyResult struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
	Valid   bool   `json:"valid"`
	// BrokenAt is the 1-based seq of the first record whose hash doesn't
	// match its own content+PrevHash, or 0 when Valid is true.
	BrokenAt int    `json:"broken_at,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// VerifyAuditLog recomputes every record's hash from its content and
// PrevHash and confirms it matches both the stored Hash and the next
// record's PrevHash — the tamper-evidence check RNF-4.10 exists for. Any
// edited, reordered, deleted, or truncated-mid-chain record is detected;
// records appended honestly after the point of tampering do not hide it,
// since the chain from the tampered record forward no longer verifies.
func VerifyAuditLog(path string) (*VerifyResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	defer f.Close()

	res := &VerifyResult{Path: path, Valid: true}
	prevHash := auditGenesisHash
	expectedSeq := 1

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var rec AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			res.Valid = false
			res.BrokenAt = expectedSeq
			res.Reason = fmt.Sprintf("record %d: not valid JSON: %v", expectedSeq, err)
			return res, nil
		}
		res.Records++

		if rec.Seq != expectedSeq {
			res.Valid = false
			res.BrokenAt = expectedSeq
			res.Reason = fmt.Sprintf("expected seq %d, got %d (a record was removed, reordered, or duplicated)", expectedSeq, rec.Seq)
			return res, nil
		}
		if rec.PrevHash != prevHash {
			res.Valid = false
			res.BrokenAt = rec.Seq
			res.Reason = fmt.Sprintf("record %d: prev_hash %q does not match the previous record's hash %q", rec.Seq, rec.PrevHash, prevHash)
			return res, nil
		}
		wantHash, err := hashRecord(rec.Seq, rec.Timestamp, rec.Event, rec.Detail, rec.PrevHash)
		if err != nil {
			return nil, fmt.Errorf("recompute hash for record %d: %w", rec.Seq, err)
		}
		if rec.Hash != wantHash {
			res.Valid = false
			res.BrokenAt = rec.Seq
			res.Reason = fmt.Sprintf("record %d: stored hash does not match its own content (edited after the fact)", rec.Seq)
			return res, nil
		}

		prevHash = rec.Hash
		expectedSeq++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read audit log %s: %w", path, err)
	}
	return res, nil
}
