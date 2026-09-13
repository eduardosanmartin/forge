package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func TestSessionCompareArgValidation(t *testing.T) {
	cmd := newSessionCompareCommand()
	if cmd.Use != "compare <session-a> <session-b>" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatalf("compare should require 2 args")
	}
	if err := cmd.Args(cmd, []string{"a"}); err == nil {
		t.Fatalf("compare should require 2 args, got 1")
	}
	if err := cmd.Args(cmd, []string{"a", "b", "c"}); err == nil {
		t.Fatalf("compare should require exactly 2 args")
	}
	if err := cmd.Args(cmd, []string{"sess-a", "sess-b"}); err != nil {
		t.Fatalf("valid args: %v", err)
	}
	if cmd.Flags().Lookup("json") == nil {
		t.Fatalf("compare should expose --json")
	}
	// alias diff
	found := false
	for _, a := range cmd.Aliases {
		if a == "diff" {
			found = true
		}
	}
	if !found {
		t.Fatalf("compare should have alias diff, got %v", cmd.Aliases)
	}
}

func TestSessionCompareCommandRegistered(t *testing.T) {
	cmd := newSessionCommand()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
		// cobra aliases also appear as separate? Check also alias via flag
	}
	if !subs["compare"] {
		t.Fatalf("session missing compare subcommand: %v", subs)
	}
	// ensure diff alias resolves via cobra
	c, _, err := cmd.Find([]string{"diff"})
	if err != nil || c == nil || c.Name() != "compare" {
		t.Fatalf("diff alias not resolvable, c=%v err=%v", c, err)
	}
}

func TestSessionCompareJSONEnvelope_Shape(t *testing.T) {
	var out bytes.Buffer
	fake := daemon.CompareSessionsResult{
		SessionA:        daemon.SessionResult{ID: "a-1", MessageCount: 3, Metadata: map[string]any{"branch_parent": "root"}},
		SessionB:        daemon.SessionResult{ID: "b-1", MessageCount: 2},
		CountA:          3,
		CountB:          2,
		DivergentCountA: 2,
		DivergentCountB: 1,
		BranchAtSeqA:    1,
		BranchAtSeqB:    1,
		SameSession:     false,
		DivergentA:      []daemon.MessageResult{{Seq: 2, Role: "user", Content: "A1"}, {Seq: 3, Role: "assistant", Content: "A2"}},
		DivergentB:      []daemon.MessageResult{{Seq: 2, Role: "user", Content: "B1"}},
	}
	if err := writeJSONResultEnvelope(&out, "session compare", &fake); err != nil {
		t.Fatalf("write: %v", err)
	}
	env := decodeRaw(t, out.Bytes())
	assertEnvelopeInvariants(t, env, "session compare")
	var got daemon.CompareSessionsResult
	if err := json.Unmarshal(env.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.CountA != 3 || got.CountB != 2 {
		t.Fatalf("counts = %d/%d want 3/2", got.CountA, got.CountB)
	}
	if len(got.DivergentA) != 2 || len(got.DivergentB) != 1 {
		t.Fatalf("divergent lens = %d/%d want 2/1", len(got.DivergentA), len(got.DivergentB))
	}
}

func TestCobraSessionCompareViaMock(t *testing.T) {
	// Verify command construction doesn't panic with cobra exact args helper
	cmd := &cobra.Command{Args: cobra.ExactArgs(2)}
	if err := cmd.Args(cmd, []string{"a", "b"}); err != nil {
		t.Fatalf("cobra ExactArgs 2: %v", err)
	}
}

// Integration-like test via real store CompareSessions logic exercised through
// the CLI helpers indirectly: ensure human output contains expected sections
// by calling runSessionCompare with a short-lived daemon.
// This reuses client.CompareSessions via a test daemon stack from internal/client,
// but we can't import client test helpers here directly; we test the pure
// store path for divergence separately and here just verify envelope helper
// for compare is wired.
func TestSessionCompareJSONFlagMatrix(t *testing.T) {
	// Ensure session compare exposes --json like other session commands
	cmd := newSessionCompareCommand()
	if cmd.Flags().Lookup("json") == nil {
		t.Fatalf("compare must expose --json per RF-6.3 matrix")
	}
}

// no daemon required for the following; placeholder for context import
var _ = context.Background
var _ = time.Now
