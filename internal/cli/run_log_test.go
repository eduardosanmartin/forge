package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetRunFlagsForTest guards against the exact flag-leakage hazard
// TestRunManifestMissingDaemonStillReportsError already documents: RootCommand
// is a shared singleton across this package's tests (execRoot reuses it), and
// pflag never resets a bound variable back to its default just because a
// later Parse() omits that flag. A test setting --manifest/--state-dir to a
// real (now-deleted) temp path must restore the defaults afterward, or an
// unrelated later test relying on "unset" state fails for a reason that has
// nothing to do with what it's actually testing.
func resetRunFlagsForTest(t *testing.T) {
	t.Helper()
	cmd, _, err := RootCommand.Find([]string{"run"})
	if err != nil {
		t.Fatalf("find run command: %v", err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"manifest", "state-dir", "log", "resume", "yes", "decompose", "verify-audit"} {
			f := cmd.Flags().Lookup(name)
			if f == nil {
				continue
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	})
}

// TestRunCommandRegistersLogFlag mirrors TestRunCommandRegistersResumeFlag:
// a basic wiring check that --log exists and defaults off.
func TestRunCommandRegistersLogFlag(t *testing.T) {
	cmd := newRunCommand()
	f := cmd.Flags().Lookup("log")
	if f == nil {
		t.Fatal("run command missing --log flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--log default = %q, want false", f.DefValue)
	}
}

// TestRunManifestLogFlagWritesActivityLog drives a real dry_run manifest
// (needs no daemon) through runManifest with --log and checks
// --state-dir/.forge/runs/<run_id>/activity.log gets created with the run's
// header and final report — the file-creation/content half of --log's
// contract. The live progress/tool-tick/checkpoint tee paths (only
// reachable via a real Executor) aren't exercised here; they share the same
// activityWriter helper this test does cover through the report path.
func TestRunManifestLogFlagWritesActivityLog(t *testing.T) {
	resetRunFlagsForTest(t)
	path := writeTestManifest(t, "dry_run")
	stateDir := t.TempDir()

	err := execRoot(t, "run", "--manifest", path, "--log", "--state-dir", stateDir, "--resume=false")
	if err != nil {
		t.Fatalf("run --manifest --log: %v", err)
	}

	logPath := filepath.Join(stateDir, ".forge", "runs", "resume-test-run", "activity.log")
	data, rErr := os.ReadFile(logPath)
	if rErr != nil {
		t.Fatalf("expected activity.log at %s: %v", logPath, rErr)
	}
	content := string(data)
	if !strings.Contains(content, "=== forge run --manifest") {
		t.Errorf("activity.log missing run header, got:\n%s", content)
	}
	if !strings.Contains(content, "resume-test-run") {
		t.Errorf("activity.log missing run_id, got:\n%s", content)
	}
}

// TestRunManifestWithoutLogFlagWritesNoFile is the inverse regression: the
// default (no --log) must not create anything under .forge/runs, matching
// forge's existing behavior before this flag existed.
func TestRunManifestWithoutLogFlagWritesNoFile(t *testing.T) {
	resetRunFlagsForTest(t)
	path := writeTestManifest(t, "dry_run")
	stateDir := t.TempDir()

	err := execRoot(t, "run", "--manifest", path, "--state-dir", stateDir, "--resume=false", "--log=false")
	if err != nil {
		t.Fatalf("run --manifest: %v", err)
	}

	logPath := filepath.Join(stateDir, ".forge", "runs", "resume-test-run", "activity.log")
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Errorf("expected no activity.log without --log, found one at %s", logPath)
	}
}

// TestOpenActivityLogAppendsAcrossInvocations checks the doc-promised
// behavior directly: a second call for the same run_id appends rather than
// truncating, so a resumed run's log reads as one continuous timeline.
func TestOpenActivityLogAppendsAcrossInvocations(t *testing.T) {
	stateDir := t.TempDir()

	f1, err := openActivityLog(stateDir, "run-1", "run", "manifest.json")
	if err != nil {
		t.Fatalf("first openActivityLog: %v", err)
	}
	f1.WriteString("first line\n")
	f1.Close()

	f2, err := openActivityLog(stateDir, "run-1", "resume", "manifest.json")
	if err != nil {
		t.Fatalf("second openActivityLog: %v", err)
	}
	f2.WriteString("second line\n")
	f2.Close()

	data, err := os.ReadFile(filepath.Join(stateDir, ".forge", "runs", "run-1", "activity.log"))
	if err != nil {
		t.Fatalf("read activity.log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "first line") || !strings.Contains(content, "second line") {
		t.Errorf("expected both invocations' content preserved (append, not truncate), got:\n%s", content)
	}
}
