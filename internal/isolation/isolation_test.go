package isolation

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestWrapCommandSetsEnvAndArgs(t *testing.T) {
	cmd := WrapCommand("/usr/local/bin/forge", "go", []string{"build", "./..."},
		WorkspaceEnvVar+"=/work/ws")

	if cmd.Path != "/usr/local/bin/forge" {
		t.Errorf("Path = %q, want the forge self path", cmd.Path)
	}
	wantArgs := []string{"/usr/local/bin/forge", "go", "build", "./..."}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("Args = %v, want %v", cmd.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if cmd.Args[i] != a {
			t.Errorf("Args[%d] = %q, want %q", i, cmd.Args[i], a)
		}
	}

	var childMarked, workspaceSet, pathKept bool
	for _, e := range cmd.Env {
		switch {
		case e == ChildEnvVar+"=1":
			childMarked = true
		case e == WorkspaceEnvVar+"=/work/ws":
			workspaceSet = true
		case strings.HasPrefix(e, "PATH="):
			pathKept = true
		}
	}
	if !childMarked {
		t.Errorf("env missing %s=1; main() would not dispatch to the wrapper", ChildEnvVar)
	}
	if !workspaceSet {
		t.Errorf("env missing %s passthrough", WorkspaceEnvVar)
	}
	if !pathKept {
		t.Error("inherited environment lost (PATH missing); child could not resolve commands")
	}
}

func TestWrapCommandWithoutExtraEnv(t *testing.T) {
	cmd := WrapCommand("forge-self", "ls", nil)
	found := false
	for _, e := range cmd.Env {
		if e == ChildEnvVar+"=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("env missing %s=1", ChildEnvVar)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "ls" {
		t.Errorf("Args = %v, want [forge-self ls]", cmd.Args)
	}
}

// TestCapabilitiesMatchesPlatform pins the compile-time degradation contract:
// full support on Linux, documented no-op everywhere else.
func TestCapabilitiesMatchesPlatform(t *testing.T) {
	cap := Capabilities()
	switch runtime.GOOS {
	case "linux":
		if !cap.OSIsolation {
			t.Fatal("linux reports OSIsolation=false; kernel checks belong at apply time")
		}
	default:
		if cap.OSIsolation {
			t.Fatal("non-linux platform claims OS isolation")
		}
		if cap.Reason == "" {
			t.Error("disabled platforms must explain why in Reason")
		}
		if !strings.Contains(cap.Reason, "permissions model") {
			t.Errorf("Reason %q should mention that the permissions model remains active", cap.Reason)
		}
	}
}

// TestIsolationEnabledRouting verifies routing: enabled wherever the
// platform supports it, never silently enabled where it cannot.
func TestIsolationEnabledRouting(t *testing.T) {
	want := Capabilities().OSIsolation
	for _, requireIso := range []bool{true, false} {
		if got := IsolationEnabled(requireIso); got != want {
			t.Errorf("IsolationEnabled(requireIso=%v) = %v, want %v",
				requireIso, got, want)
		}
	}
}

func TestWorkspaceEnvVarDistinctFromChildVar(t *testing.T) {
	if ChildEnvVar == WorkspaceEnvVar {
		t.Fatal("workspace and child markers must be distinct variables")
	}
	if os.Getenv(ChildEnvVar) == "1" && os.Getenv(WorkspaceEnvVar) != "" {
		// Guard against running the whole suite inside a wrapper by accident.
		t.Skip("test suite itself is running as an isolation wrapper child")
	}
}
