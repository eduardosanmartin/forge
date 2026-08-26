//go:build windows

// Process-group control for spawned shell commands on Windows. Windows has
// no Unix process groups; killing is limited to the direct child via
// TerminateProcess. Grandchildren of a timed-out command may briefly
// outlive it — a pre-existing platform limitation, unchanged by this
// package (full tree reaping on Windows would require Job Objects).
package tools

import (
	"os"
	"os/exec"
)

// configureProcessGroup is a no-op on Windows: there is no setpgid
// equivalent reachable through os/exec.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessTree terminates the direct process. Safe to call on an exited
// or never-started process; errors are best-effort.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
