package daemon

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// newManagerWithRealShell builds a SessionManager over the REAL tools
// registry and permission engine, so doneCriteriaCommandRunner is exercised
// end to end through shell_exec's policy check.
func newManagerWithRealShell(t *testing.T, shellAllow []string) *SessionManager {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := perms.New(perms.PermissionsPolicy{Shell: perms.ShellPermissions{Allow: shellAllow}}, wd, logger)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(eng, wd, logger)
	st := newTestStore()
	return NewSessionManager(st, newTestLLMRegistry(), reg, NewEmergencyState(logger), logger, config.Defaults(), eng, st)
}

// C1 regression: a done_criteria program outside permissions.shell.allow
// must be refused by the policy and never executed.
func TestDoneCriteriaCommandRunner_DeniedByPolicy(t *testing.T) {
	m := newManagerWithRealShell(t, nil) // deny-by-default: nothing allowed
	run := m.doneCriteriaCommandRunner("")
	if run == nil {
		t.Fatal("expected a command runner when a tools registry is wired")
	}
	_, _, denied, err := run(context.Background(), "go", []string{"version"})
	if err != nil {
		t.Fatalf("unexpected infrastructure error: %v", err)
	}
	if denied == "" {
		t.Fatal("expected the policy to deny go when shell.allow is empty")
	}
}

func TestDoneCriteriaCommandRunner_AllowedReportsExitCode(t *testing.T) {
	m := newManagerWithRealShell(t, []string{"go"})
	run := m.doneCriteriaCommandRunner("")

	_, code, denied, err := run(context.Background(), "go", []string{"version"})
	if err != nil || denied != "" || code != 0 {
		t.Fatalf("go version: code=%d denied=%q err=%v, want success", code, denied, err)
	}
	_, code, denied, err = run(context.Background(), "go", []string{"__not_a_real_subcommand__"})
	if err != nil || denied != "" || code == 0 {
		t.Fatalf("bad subcommand: code=%d denied=%q err=%v, want non-zero exit", code, denied, err)
	}
}
