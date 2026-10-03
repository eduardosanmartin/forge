package tools

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessTree terminates p AND every process it spawned. Killing only
// the direct child (the previous behavior) left grandchildren running on
// timeout or emergency halt — e.g. "npm test" keeps its node process alive
// after npm itself is killed. taskkill /T walks the tree by parent PID,
// which is still intact here because the root is alive when we kill it.
// Falls back to killing the direct child if taskkill is unavailable.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	done := make(chan error, 1)
	if err := kill.Start(); err == nil {
		go func() { done <- kill.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				return nil
			}
		case <-time.After(5 * time.Second):
			_ = kill.Process.Kill()
		}
	}
	return p.Kill()
}
