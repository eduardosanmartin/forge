package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/run"
)

func TestRunVerifyAuditCleanChain(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, "checkpoint") // run_id: resume-test-run
	seedCLIAuditLog(t, dir, "resume-test-run", 3)

	var buf bytes.Buffer
	if err := runVerifyAudit(&buf, manifestPath, dir, false); err != nil {
		t.Fatalf("runVerifyAudit: %v", err)
	}
	if !strings.Contains(buf.String(), "OK:") {
		t.Errorf("expected an OK message, got %q", buf.String())
	}
}

func TestRunVerifyAuditTamperedChain(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, "checkpoint")
	seedCLIAuditLog(t, dir, "resume-test-run", 2)

	auditPath := filepath.Join(dir, ".forge", "runs", "resume-test-run", "audit.jsonl")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	tampered := strings.Replace(string(data), `"seeded"`, `"seeded_EDITED"`, 1)
	if tampered == string(data) {
		t.Fatal("tamper substitution did not match anything in the seeded log")
	}
	if err := os.WriteFile(auditPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("rewrite audit log: %v", err)
	}

	var buf bytes.Buffer
	err = runVerifyAudit(&buf, manifestPath, dir, false)
	if err == nil {
		t.Fatal("expected an error for a tampered audit log")
	}
	if !strings.Contains(buf.String(), "TAMPERED:") {
		t.Errorf("expected a TAMPERED message, got %q", buf.String())
	}
}

func TestRunVerifyAuditMissingLog(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, "checkpoint")

	var buf bytes.Buffer
	err := runVerifyAudit(&buf, manifestPath, dir, false)
	if err == nil {
		t.Fatal("expected an error when no audit log exists")
	}
	if !strings.Contains(buf.String(), "no audit log") {
		t.Errorf("expected a helpful missing-log message, got %q", buf.String())
	}
}

func TestRunVerifyAuditJSONOutput(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, "checkpoint")
	seedCLIAuditLog(t, dir, "resume-test-run", 1)

	var buf bytes.Buffer
	if err := runVerifyAudit(&buf, manifestPath, dir, true); err != nil {
		t.Fatalf("runVerifyAudit: %v", err)
	}
	if !strings.Contains(buf.String(), `"valid": true`) {
		t.Errorf("expected JSON envelope with valid:true, got %q", buf.String())
	}
}

func TestRunResumeAndVerifyAuditMutuallyExclusive(t *testing.T) {
	path := writeTestManifest(t, "checkpoint")
	err := execRoot(t, "run", "--manifest", path, "--resume", "--verify-audit")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UsageError, got %v", err)
	}
	if !strings.Contains(ue.Error(), "mutually exclusive") {
		t.Errorf("expected a mutual-exclusion message, got %v", ue)
	}
}

func TestRunVerifyAuditRequiresManifestFlag(t *testing.T) {
	// --manifest=/--resume=false explicitly reset those flags: RootCommand
	// and its flag variables are process-wide singletons shared across
	// every execRoot call in this package's test binary, so an earlier
	// test that passed --manifest <path> or --resume would otherwise leak
	// into this one.
	err := execRoot(t, "run", "--manifest=", "--resume=false", "--verify-audit", "hello world")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UsageError, got %v", err)
	}
	if !strings.Contains(ue.Error(), "--verify-audit requires --manifest") {
		t.Errorf("usage message should name the missing --manifest flag, got %v", ue)
	}
}

// seedCLIAuditLog writes n clean hash-chained records for runID under dir,
// mirroring what a real regulado/datos-sensibles Run would have produced.
func seedCLIAuditLog(t *testing.T, dir, runID string, n int) {
	t.Helper()
	al, err := run.OpenAuditLog(dir, runID)
	if err != nil {
		t.Fatalf("OpenAuditLog: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := al.Append("seeded", map[string]any{"i": i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := al.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
