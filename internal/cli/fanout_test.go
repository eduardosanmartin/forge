package cli

import (
	"context"
	"io"
	"reflect"
	"testing"
)

func TestFanoutCommandConstruction(t *testing.T) {
	cmd := newFanoutCommand()
	if cmd.Use != "fanout <task>" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, name := range []string{"models", "session", "json"} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Fatalf("fanout must expose --%s", name)
		}
	}
	// Argument validation via RunE wiring: fanout takes at least one arg.
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("fanout requires a task argument")
	}
}

func TestParseModelList(t *testing.T) {
	table := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "two entries", raw: "alpha/m1,beta/m2", want: []string{"alpha/m1", "beta/m2"}},
		{name: "spaces trimmed", raw: "a/m1 , b/m2", want: []string{"a/m1", "b/m2"}},
		{name: "empty raw yields nil", raw: "", want: nil},
		{name: "empty commas dropped", raw: "a/m1,,,b/m2,", want: []string{"a/m1", "b/m2"}},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			got := parseModelList(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseModelList(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestRunFanoutArgValidation(t *testing.T) {
	// No daemon runs in this test: failures must come from local validation
	// and never from a connection attempt.
	table := []struct {
		name   string
		task   string
		models []string
	}{
		{name: "empty task rejected", task: "   ", models: []string{"a/m1"}},
		{name: "missing --models rejected", task: "task", models: nil},
		{name: "whitespace model entry rejected", task: "task", models: []string{" "}},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			err := runFanout(context.Background(), io.Discard, tc.task, tc.models, "", 0, 0, false)
			if err == nil {
				t.Fatalf("want arg validation error for %s", tc.name)
			}
		})
	}
}
