package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestSessionReplayArgValidation(t *testing.T) {
	cmd := newSessionReplayCommand()
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatalf("replay should require 1 arg")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatalf("replay should require exactly 1 arg")
	}
	if err := cmd.Args(cmd, []string{"sess-123"}); err != nil {
		t.Fatalf("valid args: %v", err)
	}
}

func TestSessionSuccessArgValidation(t *testing.T) {
	cmd := newSessionSuccessCommand()
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatalf("success should require 1 arg")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatalf("success should require exactly 1")
	}
}

func TestSessionSuccessCommandHelp(t *testing.T) {
	cmd := newSessionSuccessCommand()
	if cmd.Use != "success <session-id>" {
		t.Fatalf("Use = %q", cmd.Use)
	}
}

func TestCobraSessionCommandsAreRegistered(t *testing.T) {
	// Verify that the root command has session subcommands registered via init().
	found := false
	for _, c := range RootCommand.Commands() {
		if c.Name() == "session" {
			found = true
			// Check subcommands exist.
			subs := map[string]bool{}
			for _, s := range c.Commands() {
				subs[s.Name()] = true
			}
			if !subs["success"] || !subs["replay"] {
				t.Fatalf("session missing subcommands: %v", subs)
			}
		}
	}
	if !found {
		t.Fatalf("RootCommand missing session command; init() may not have run")
	}
	// Also check skill mine is registered.
	foundMine := false
	for _, c := range RootCommand.Commands() {
		if c.Name() == "skill" {
			for _, s := range c.Commands() {
				if s.Name() == "mine" {
					foundMine = true
				}
			}
		}
	}
	if !foundMine {
		t.Fatalf("skill mine not registered")
	}
}

// Ensure that cobra's argument validation plus our handler's error handling
// would surface a clear error for unknown session (actual DAO test covers handler).
func TestReplayCommandArgCountViaCobra(t *testing.T) {
	cmd := &cobra.Command{Args: cobra.ExactArgs(1)}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("expected arg error")
	}
}
