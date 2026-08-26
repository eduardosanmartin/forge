//go:build unix

// Unix-only assertions for the process-group kill plumbing (RNF-4.7
// timeout hardening). Windows compiles the no-op contract instead.
package tools

import (
	"os/exec"
	"testing"
)

// TestConfigureProcessGroupSetsUnixGroup pins that spawned shells become
// their own process-group leaders, so timeout kills cover grandchildren.
func TestConfigureProcessGroupSetsUnixGroup(t *testing.T) {
	cmd := exec.Command("forge-test-nonexistent-binary")
	configureProcessGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("configureProcessGroup left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("SysProcAttr.Setpgid not set; timeout would orphan grandchildren")
	}
}
