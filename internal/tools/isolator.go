// Isolator wiring: connects the shell tool to the OS-isolation layer
// (internal/isolation, spec RNF-4.7). A nil Isolator means legacy direct
// execution — the pre-isolation behavior, kept for tests and for platforms
// where isolation is unavailable.
package tools

import (
	"os/exec"

	"github.com/eduardosanmartin/forge/internal/isolation"
)

// Isolator abstracts whether shell commands are routed through the
// OS-isolation wrapper and how that wrapper is invoked.
type Isolator interface {
	// Enabled reports whether spawned shells should be wrapped. When true,
	// Wrap must produce a command whose execution applies isolation before
	// running the user command.
	Enabled() bool

	// Wrap builds the wrapper invocation for command+args. selfExe is the
	// forge binary path to re-exec (the caller resolves it once per exec).
	Wrap(selfExe string, command string, args []string) *exec.Cmd
}

// OsIsolator is the production Isolator: it routes through forge's own
// binary acting as the isolation wrapper child (Landlock + seccomp on
// Linux; a documented no-op elsewhere).
type OsIsolator struct {
	selfExe       string
	workspaceRoot string
	enabled       bool
}

// NewOsIsolator captures the platform capability at construction time so
// routing decisions stay stable across the daemon's lifetime.
func NewOsIsolator(selfExe, workspaceRoot string) *OsIsolator {
	return &OsIsolator{
		selfExe:       selfExe,
		workspaceRoot: workspaceRoot,
		enabled:       isolation.Capabilities().OSIsolation,
	}
}

func (i *OsIsolator) Enabled() bool { return i.enabled }

// Wrap delegates to isolation.WrapCommand and passes the workspace root so
// the child can grant Landlock write access exactly there.
func (i *OsIsolator) Wrap(selfExe string, command string, args []string) *exec.Cmd {
	exe := i.selfExe
	if selfExe != "" {
		exe = selfExe
	}
	return isolation.WrapCommand(exe, command, args,
		isolation.WorkspaceEnvVar+"="+i.workspaceRoot)
}
