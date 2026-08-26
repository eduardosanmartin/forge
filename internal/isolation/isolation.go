// Package isolation provides OS-level sandboxing for shell commands spawned
// by forge (spec RNF-4.7). On Linux, shell children are re-executed through
// the forge binary itself as an isolation wrapper: the child process applies
// a Landlock filesystem ruleset plus a default-deny seccomp BPF filter and
// then replaces its own image with the requested command (unix.Exec). Both
// restrictions persist across execve, so the filters bound the user command
// — never the forge daemon, which stays unrestricted.
//
// On every other platform the package degrades to a documented no-op: the
// permission model (RNF-4.1) remains the only enforcement layer. This mirrors
// spec §6: macOS v0 is permissions-only because Apple's sandbox-exec is an
// undocumented, deprecated API and is not an acceptable foundation.
package isolation

import (
	"os"
	"os/exec"
)

// Capability describes whether this platform can enforce OS-level shell
// isolation. OSIsolation is true only when the platform (and, on Linux, the
// running kernel) is known to support the full Landlock + seccomp pair;
// Reason carries a human-readable explanation whenever OSIsolation is false.
type Capability struct {
	OSIsolation bool
	Reason      string
}

// ChildEnvVar marks a forge process as an isolation wrapper child. The
// parent sets it to "1" before exec'ing the forge binary; main() checks it
// before any CLI processing and routes to RunSelfIsolated.
const ChildEnvVar = "FORGE_ISOLATION_CHILD"

// WorkspaceEnvVar carries the workspace root from parent to wrapper child so
// the child can grant Landlock write access exactly there. When unset, the
// child falls back to its working directory.
const WorkspaceEnvVar = "FORGE_ISOLATION_WORKSPACE"

// Capabilities reports the platform's isolation support. It is resolved at
// compile time per GOOS (see the platform files); kernel-level shortfalls
// surface as actionable errors at apply time rather than here, so a caller
// can still distinguish "never possible on this OS" from "this particular
// kernel is too old".
func Capabilities() Capability { return detectCapabilities() }

// IsolationEnabled decides whether shell children spawned now would be
// routed through the isolation wrapper. Isolation is enabled whenever the
// platform supports it — it is defense-in-depth and applies regardless of
// configuration. The requireIso parameter does not change routing: when the
// platform cannot isolate and requireIso is set, the caller must refuse the
// execution (surfaced as a denial by the tools layer) instead of silently
// degrading to a bare exec.
func IsolationEnabled(requireIso bool) bool {
	return Capabilities().OSIsolation
}

// WrapCommand builds the wrapper invocation: the forge binary itself
// (selfExe) executed with ChildEnvVar=1 and the user command as trailing
// arguments, so main() dispatches to the isolation child path before any CLI
// flag parsing. extraEnv entries are appended after the inherited
// environment (later entries win on duplicates in practice).
//
// The returned Cmd is plain (no context attached); the caller owns timeout
// and process-group handling.
func WrapCommand(selfExe string, command string, args []string, extraEnv ...string) *exec.Cmd {
	env := make([]string, 0, len(os.Environ())+1+len(extraEnv))
	env = append(env, os.Environ()...)
	env = append(env, ChildEnvVar+"=1")
	env = append(env, extraEnv...)

	cmdArgs := make([]string, 0, 2+len(args))
	cmdArgs = append(cmdArgs, selfExe, command)
	cmdArgs = append(cmdArgs, args...)

	return &exec.Cmd{
		Path: selfExe,
		Args: cmdArgs,
		Env:  env,
	}
}
