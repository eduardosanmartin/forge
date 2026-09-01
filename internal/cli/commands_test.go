package cli

import (
	"testing"
)

func TestPluginCommandConstruction(t *testing.T) {
	cmd := newPluginCommand()
	if cmd.Use != "plugin" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"new", "validate", "install", "list", "enable", "disable", "remove"} {
		if !subs[want] {
			t.Fatalf("missing subcommand %q", want)
		}
	}
}

func TestSkillCommandConstruction(t *testing.T) {
	cmd := newSkillCommand()
	if cmd.Use != "skill" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"new", "validate", "install", "list", "enable", "disable", "remove"} {
		if !subs[want] {
			t.Fatalf("missing subcommand %q", want)
		}
	}
}

func TestPluginValidateArgs(t *testing.T) {
	cmd := newPluginValidateCommand()
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("validate should require 1 arg")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("validate should require exactly 1 arg")
	}
	if err := cmd.Args(cmd, []string{"path"}); err != nil {
		t.Fatalf("validate args valid: %v", err)
	}
}

func TestPluginEnableArgs(t *testing.T) {
	cmd := newPluginEnableCommand()
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("enable should require 1 arg")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("enable should require exactly 1")
	}
}

func TestServeFlagExists(t *testing.T) {
	cmd := newServeCommand()
	flag := cmd.Flags().Lookup("approve-external-plugins")
	if flag == nil {
		t.Fatal("serve should have --approve-external-plugins flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("default should be false, got %q", flag.DefValue)
	}
}
