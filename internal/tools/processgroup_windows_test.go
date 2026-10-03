package tools

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// pingCount counts running PING.EXE processes (tasklist image filter).
func pingCount(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq PING.EXE", "/NH").Output()
	if err != nil {
		t.Skipf("tasklist unavailable: %v", err)
	}
	return strings.Count(strings.ToUpper(string(out)), "PING.EXE")
}

// Regression: on timeout/halt only the direct child was killed, orphaning
// grandchildren. cmd.exe -> ping.exe models "npm -> node".
func TestKillProcessTreeKillsGrandchildren(t *testing.T) {
	before := pingCount(t)
	cmd := exec.Command("cmd", "/c", "ping -n 60 127.0.0.1 >NUL")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pingCount(t) <= before {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Skip("grandchild ping never appeared")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := killProcessTree(cmd.Process); err != nil {
		t.Fatalf("killProcessTree: %v", err)
	}
	_ = cmd.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for pingCount(t) > before {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild ping.exe still running after killProcessTree")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
