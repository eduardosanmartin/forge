package main

import "testing"

// TestMaybeRunIsolationChildNotMarked verifies the daemon path is untouched:
// without the marker the helper must be a no-op returning false.
func TestMaybeRunIsolationChildNotMarked(t *testing.T) {
	called := false
	run := func([]string) error { called = true; return nil }
	fatal := func(int) { t.Fatal("fatal must not fire when the marker is absent") }

	got := maybeRunIsolationChild([]string{"serve"}, func(string) string { return "" }, run, fatal)
	if got {
		t.Fatal("returned true without FORGE_ISOLATION_CHILD=1; CLI flow would be hijacked")
	}
	if called {
		t.Error("run invoked although no wrapper marker was set")
	}
}

// TestMaybeRunIsolationChildMarkedRuns verifies dispatch: with the marker,
// argv reaches the isolation entry point and normal flow stops.
func TestMaybeRunIsolationChildMarkedRuns(t *testing.T) {
	var gotArgv []string
	run := func(argv []string) error { gotArgv = argv; return nil }
	fatal := func(int) { t.Fatal("fatal fired on successful run") }

	got := maybeRunIsolationChild(
		[]string{"go", "build", "./..."},
		func(k string) string {
			if k == "FORGE_ISOLATION_CHILD" {
				return "1"
			}
			return ""
		},
		run, fatal,
	)
	if !got {
		t.Fatal("returned false although marked as wrapper child")
	}
	if len(gotArgv) != 3 || gotArgv[0] != "go" || gotArgv[2] != "./..." {
		t.Errorf("argv = %v, want passthrough [go build ./...]", gotArgv)
	}
}

// TestMaybeRunIsolationChildFailureExits126 pins the failure contract: a
// wrapper setup error reports 126 on stderr and never falls through to CLI.
func TestMaybeRunIsolationChildFailureExits126(t *testing.T) {
	exitCode := 0
	fatal := func(code int) { exitCode = code }

	got := maybeRunIsolationChild(
		[]string{"true"},
		func(string) string { return "1" },
		func([]string) error { return errIsolationStub },
		fatal,
	)
	if !got {
		t.Fatal("returned false despite marker; error path lost")
	}
	if exitCode != 126 {
		t.Errorf("exit code = %d, want 126", exitCode)
	}
}

var errIsolationStub = &isolationError{}

type isolationError struct{}

func (*isolationError) Error() string { return "stub failure" }
