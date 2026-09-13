package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMemoryCommandConstruction(t *testing.T) {
	cmd := newMemoryCommand()
	if cmd.Use != "memory" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"list", "get", "add", "edit", "delete"} {
		if !subs[want] {
			t.Fatalf("missing subcommand %q", want)
		}
	}
}

func TestMemoryCommandArgs(t *testing.T) {
	table := []struct {
		name    string
		cmdF    func() *cobra.Command
		args    []string
		wantErr bool
	}{
		{"get rejects 0 args", newMemoryGetCommand, []string{}, true},
		{"get rejects 2 args", newMemoryGetCommand, []string{"1", "2"}, true},
		{"get accepts 1 arg", newMemoryGetCommand, []string{"7"}, false},
		{"edit rejects 0 args", newMemoryEditCommand, []string{}, true},
		{"delete rejects 2 args", newMemoryDeleteCommand, []string{"1", "2"}, true},
		{"add requires content", newMemoryAddCommand, []string{}, true},
		{"add accepts free-form content", newMemoryAddCommand, []string{"multi", "word"}, false},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmdF()
			err := cmd.Args(cmd, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Args = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestMemoryFlagsExist(t *testing.T) {
	if f := newMemoryListCommand().Flags().Lookup("session"); f == nil {
		t.Fatal("memory list must expose --session")
	}
	if f := newMemoryListCommand().Flags().Lookup("json"); f == nil {
		t.Fatal("memory list must expose --json (RF-6.3)")
	}
	add := newMemoryAddCommand()
	for _, name := range []string{"session", "source", "tags", "json"} {
		if f := add.Flags().Lookup(name); f == nil {
			t.Fatalf("memory add must expose --%s", name)
		}
	}
	edit := newMemoryEditCommand()
	for _, name := range []string{"content", "source", "tags", "json"} {
		if f := edit.Flags().Lookup(name); f == nil {
			t.Fatalf("memory edit must expose --%s", name)
		}
	}
	if f := newMemoryDeleteCommand().Flags().Lookup("json"); f == nil {
		t.Fatal("memory delete must expose --json (RF-6.3)")
	}
}

func TestParseAnchorID(t *testing.T) {
	table := []struct {
		name    string
		raw     string
		wantID  int64
		wantErr bool
	}{
		{name: "positive id", raw: "42", wantID: 42},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "negative rejected", raw: "-3", wantErr: true},
		{name: "non numeric rejected", raw: "abc", wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			id, err := parseAnchorID(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if id != tc.wantID {
				t.Fatalf("id = %d, want %d", id, tc.wantID)
			}
		})
	}
}

func TestBuildMemoryEditorPatchSemantics(t *testing.T) {
	cmd := newMemoryEditCommand()

	t.Run("no flags passed means no patch fields", func(t *testing.T) {
		e := buildMemoryEditor("", "", nil, cmd)
		if e.content != nil || e.source != nil || e.tags != nil {
			t.Fatalf("all fields must be nil when no flags passed: %+v", e)
		}
	})
	t.Run("passed empty --tags clears tags as empty list", func(t *testing.T) {
		if err := cmd.Flags().Set("tags", ""); err != nil {
			t.Fatalf("set --tags: %v", err)
		}
		e := buildMemoryEditor("", "", []string{}, cmd)
		if e.tags == nil {
			t.Fatal("explicit empty --tags must produce an empty (not nil) tag patch")
		}
		if len(*e.tags) != 0 {
			t.Fatalf("explicit empty --tags must be an empty list, got %v", *e.tags)
		}
	})
	t.Run("changed content and source are set", func(t *testing.T) {
		cmd := newMemoryEditCommand()
		if err := cmd.Flags().Set("content", "new content"); err != nil {
			t.Fatalf("set --content: %v", err)
		}
		if err := cmd.Flags().Set("source", "auto"); err != nil {
			t.Fatalf("set --source: %v", err)
		}
		e := buildMemoryEditor("new content", "auto", nil, cmd)
		if e.content == nil || *e.content != "new content" {
			t.Fatalf("content patch missing: %+v", e.content)
		}
		if e.source == nil || *e.source != "auto" {
			t.Fatalf("source patch missing: %+v", e.source)
		}
		if e.tags != nil {
			t.Fatalf("untouched tags must stay nil, got %v", e.tags)
		}
	})
}

func TestRunMemoryGetRejectsBadIDBeforeConnecting(t *testing.T) {
	// No daemon runs in this test; the command must fail at arg parsing and
	// never attempt connection.
	for _, runner := range []func(ctx context.Context, out io.Writer, rawID string, jsonOut bool) error{
		runMemoryGet,
		runMemoryDelete,
	} {
		err := runner(context.Background(), io.Discard, "not-a-number", false)
		if err == nil || !strings.Contains(err.Error(), "anchor id") {
			t.Fatalf("bad id must be rejected locally, got %v", err)
		}
	}
}
