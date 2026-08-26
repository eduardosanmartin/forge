//go:build unix

// Process-group control for spawned shell commands on Unix. The child is
// made its own process-group leader so a timeout can SIGKILL the entire
// tree — wrapper children included — instead of orphaning grandchildren.
package tools

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup makes cmd start as its own process group
// (setpgid(0,0) in the child), enabling negative-pgid kills.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree terminates p's whole process group. Safe to call on an
// exited or never-started process; errors are best-effort (the caller is
// usually racing process exit during timeout handling).
func killProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	// Negative pid targets the process group; with Setpgid the direct
	// child IS the leader, so this covers the whole spawned tree.
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
