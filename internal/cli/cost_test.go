package cli

import (
	"errors"
	"testing"

	"github.com/eduardosanmartin/forge/internal/client"
)

func TestSessionCostCommandMissingDaemonHintsForgeServe(t *testing.T) {
	err := execRoot(t, "session", "cost", "some-session-id")
	if !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Fatalf("want ErrDaemonNotRunning chain, got %v", err)
	}
}

func TestCostSummaryCommandMissingDaemonHintsForgeServe(t *testing.T) {
	err := execRoot(t, "cost", "summary")
	if !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Fatalf("want ErrDaemonNotRunning chain, got %v", err)
	}
}

func TestSessionCostCommandRequiresSessionID(t *testing.T) {
	err := execRoot(t, "session", "cost")
	if err == nil {
		t.Fatal("expected an error when session-id is missing")
	}
}

func TestCostCommandRegistersSummarySubcommand(t *testing.T) {
	cmd := newCostCommand()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "summary" {
			found = true
		}
	}
	if !found {
		t.Error("cost command missing summary subcommand")
	}
}

func TestSessionCommandRegistersCostSubcommand(t *testing.T) {
	cmd := newSessionCommand()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "cost" {
			found = true
		}
	}
	if !found {
		t.Error("session command missing cost subcommand")
	}
}
