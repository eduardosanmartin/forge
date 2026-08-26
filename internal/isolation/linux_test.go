//go:build linux

package isolation

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"golang.org/x/sys/unix"
)

// TestMain doubles as the re-exec target for the smoke tests: ApplyAndExec
// replaces the process image on success, so it can only be observed from a
// parent process. The parent re-runs this test binary with
// FORGE_ISOLATION_TEST_CHILD set; the child applies filters and execs, and
// its exit status is the assertion.
func TestMain(m *testing.M) {
	switch mode := os.Getenv("FORGE_ISOLATION_TEST_CHILD"); mode {
	case "smoke-ok":
		ws := os.Getenv("FORGE_ISOLATION_TEST_WS")
		if err := ApplyAndExec("/bin/true", nil, ws); err != nil {
			fmt.Fprintf(os.Stderr, "smoke-ok child: %v\n", err)
			os.Exit(126)
		}
		os.Exit(127) // unreachable: exec replaced the image

	case "smoke-deny-write":
		ws := os.Getenv("FORGE_ISOLATION_TEST_WS")
		outside := os.Getenv("FORGE_ISOLATION_TEST_OUTSIDE")
		if err := ApplyAndExec("/bin/sh", []string{"-c", `printf x > "$1"`, "forge-test", outside}, ws); err != nil {
			fmt.Fprintf(os.Stderr, "smoke-deny child failed before exec: %v\n", err)
			os.Exit(126)
		}
		os.Exit(127) // unreachable

	default:
		os.Exit(m.Run())
	}
}

func TestCapabilitiesTrue(t *testing.T) {
	cap := Capabilities()
	if !cap.OSIsolation {
		t.Fatal("linux must report OSIsolation=true; kernel checks happen at apply time")
	}
}

func TestAssembleFilter(t *testing.T) {
	n, err := assembleFilterInstructionCount()
	if err != nil {
		t.Fatalf("assemble seccomp policy: %v", err)
	}
	if n == 0 {
		t.Fatal("assembled filter is empty")
	}

	// Baseline intersection must drop names missing from this GOARCH's
	// table and keep the ones present.
	if got := intersectBaseline([]string{"read", "no_such_syscall_xyz"}); len(got) != 1 || got[0] != "read" {
		t.Errorf("intersectBaseline = %v, want [read]", got)
	}
	total := 0
	for _, group := range baselineSyscallGroups() {
		total += len(intersectBaseline(group))
	}
	if total < 100 {
		t.Errorf("allowlist collapsed to %d syscalls on %s; baseline lost",
			total, runtime.GOARCH)
	}
}

func TestLandlockUnsupportedKernelMessage(t *testing.T) {
	err := landlockProbeError(unix.ENOSYS)
	if !strings.Contains(err.Error(), "landlock unsupported") {
		t.Errorf("ENOSYS error %q must contain \"landlock unsupported\"", err.Error())
	}
	if !strings.Contains(err.Error(), "5.13") {
		t.Errorf("ENOSYS error %q should name the minimum kernel (5.13)", err.Error())
	}
	if err := landlockProbeError(unix.EINVAL); !strings.Contains(err.Error(), "landlock unsupported") {
		t.Errorf("EINVAL error %q must contain \"landlock unsupported\"", err.Error())
	}
	if err := landlockProbeError(unix.EACCES); strings.Contains(err.Error(), "landlock unsupported") {
		t.Errorf("unexpected errno should not be classified as unsupported: %v", err)
	}
}

// TestPathInsideRuleDedup pins the de-duplication helper used to avoid
// double-adding rules for temp dirs nested inside the workspace.
func TestPathInsideRuleDedup(t *testing.T) {
	root := "/tmp/ws-root"
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/ws-root", true},
		{"/tmp/ws-root/inner/file", true},
		{"/tmp/ws-rother", false},
		{"/tmp/ws-root-evilmount", false},
		{"/etc/passwd", false},
	}
	for _, tc := range cases {
		if got := pathInside(tc.path, root); got != tc.want {
			t.Errorf("pathInside(%q, %q) = %v, want %v", tc.path, root, got, tc.want)
		}
	}
}

func isolationKernelReady(t *testing.T) {
	t.Helper()
	abi, err := landlockABIVersion()
	if err != nil || abi < 1 {
		t.Skipf("kernel lacks Landlock (needs Linux >= 5.13): %v", err)
	}
	if !seccomp.Supported() {
		t.Skip("kernel lacks seccomp filter support")
	}
}

// runIsolationChild re-executes the test binary as an isolation wrapper
// child and reports combined output plus exit code.
func runIsolationChild(t *testing.T, mode, ws, outside string) (string, int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	cmd := exec.Command(self, "-test.run=^$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		"FORGE_ISOLATION_TEST_CHILD="+mode,
		"FORGE_ISOLATION_TEST_WS="+ws,
		"FORGE_ISOLATION_TEST_OUTSIDE="+outside,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run isolation child: %v", err)
	}
	return string(out), code
}

func TestApplyAndExecSmoke(t *testing.T) {
	isolationKernelReady(t)

	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Positive control: /bin/true through the full wrapper must exit 0,
	// proving the filters do not break plain execution.
	out, code := runIsolationChild(t, "smoke-ok", ws, "")
	if code != 0 {
		t.Fatalf("/bin/true through isolation exited %d; output:\n%s", code, out)
	}
}

func TestApplyAndExecDeniesWriteOutsideWorkspace(t *testing.T) {
	isolationKernelReady(t)

	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Target outside BOTH the workspace and the granted temp dir. Home is
	// not in any RW rule; skip if the environment collapses these trees.
	tmpGranted := os.TempDir()
	outside := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" &&
		!pathInside(home, tmpGranted) && !pathInside(home, base) {
		outside = filepath.Join(home, ".forge-isolation-deny-probe")
	} else if !pathInside("/var/tmp", tmpGranted) && !pathInside("/var/tmp", base) {
		outside = "/var/tmp/forge-isolation-deny-probe"
	}
	if outside == "" {
		t.Skip("no probe path outside both workspace and granted temp dir")
	}

	out, code := runIsolationChild(t, "smoke-deny-write", ws, outside)
	if code == 0 {
		t.Fatalf("write to %s succeeded under isolation; Landlock did not bound the child; output:\n%s", outside, out)
	}
	if !strings.Contains(strings.ToLower(out), "permission denied") {
		t.Logf("child output lacked EACCES wording (still denied, exit %d):\n%s", code, out)
	}
}
