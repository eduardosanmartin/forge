//go:build linux

// Linux half of the isolation package (spec RNF-4.7): shell children are
// bounded by a Landlock filesystem ruleset plus a default-deny seccomp BPF
// filter, applied to the re-executed forge child before it replaces its own
// image with the user command (unix.Exec). Both restrictions survive
// execve because the process sets PR_SET_NO_NEW_PRIVS first, so they bound
// the spawned developer tooling while leaving the forge daemon untouched.
//
// Landlock is driven through golang.org/x/sys/unix raw syscalls (the module
// ships the attribute types, constants, and syscall numbers; wrapper
// functions are not provided), following the kernel's documented ABI
// probing recipe. Seccomp assembly uses github.com/elastic/go-seccomp-bpf,
// which validates every syscall name against the running GOARCH table.
package isolation

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/sys/unix"
)

// landlockCreateRulesetVersion is the flag that turns
// landlock_create_ruleset(2) into an ABI-version query (kernel docs:
// Documentation/userspace-api/landlock.rst).
const landlockCreateRulesetVersion = 0x0001

// detectCapabilities reports full OS-level isolation support. Kernel-level
// shortfalls (pre-5.13 kernels, hardened seccomp policies) surface as
// actionable errors at ApplyAndExec time instead of here, so callers can
// distinguish "impossible on this OS" from "this particular kernel".
func detectCapabilities() Capability {
	return Capability{OSIsolation: true}
}

// RunSelfIsolated is the wrapper-child entry point dispatched by main()
// before any CLI processing. args carries the user command as its first
// element followed by its arguments. On success the current process image
// is replaced and this function never returns.
func RunSelfIsolated(args []string) error {
	if len(args) < 1 {
		return errors.New("isolation child invoked without a command")
	}

	workspace := os.Getenv(WorkspaceEnvVar)
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("isolation child: resolve workspace: %w", err)
		}
		workspace = cwd
	}

	target, err := LookPathInChild(args[0])
	if err != nil {
		return err
	}

	return ApplyAndExec(target, args[1:], workspace)
}

// LookPathInChild resolves a bare command name against PATH inside the
// wrapper child. unix.Exec performs no PATH resolution, so bare names such
// as "go" must be resolved before exec'ing.
func LookPathInChild(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("isolation child: resolve %q: %w", command, err)
	}
	return path, nil
}

// ApplyAndExec applies the Landlock ruleset and the seccomp filter to the
// current process and then replaces its image with target. Order matters:
// Landlock first, because the seccomp default-deny policy does not allow
// the landlock_* syscalls (they are pointless after restriction anyway);
// seccomp last, so nothing runs outside the filter afterwards. On success
// this function never returns.
func ApplyAndExec(target string, args []string, workspaceRoot string) error {
	// PR_SET_NO_NEW_PRIVS is required before landlock_restrict_self(2) and
	// lets an unprivileged process install seccomp filters; it also
	// guarantees both filters persist across execve.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("isolation: set no_new_privs: %w", err)
	}

	if err := applyLandlock(workspaceRoot); err != nil {
		return err
	}
	if err := applySeccomp(); err != nil {
		return err
	}

	execArgs := make([]string, 0, 1+len(args))
	execArgs = append(execArgs, target)
	execArgs = append(execArgs, args...)

	if err := unix.Exec(target, execArgs, os.Environ()); err != nil {
		return fmt.Errorf("isolation: exec %s: %w", target, err)
	}
	return nil // unreachable: unix.Exec replaces the image on success
}

// --- Landlock ------------------------------------------------------------

// landlockABIVersion queries the kernel's Landlock ABI version. Version 1
// corresponds to Linux 5.13, 2 to 5.19 (REFER), 3 to 6.2 (TRUNCATE), 4 to
// 6.7 (IOCTL_DEV). An error whose message contains "landlock unsupported"
// means the running kernel cannot enforce Landlock at all.
func landlockABIVersion() (int, error) {
	r1, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0, landlockCreateRulesetVersion, 0, 0, 0,
	)
	if errno == 0 {
		return int(r1), nil
	}
	return 0, landlockProbeError(errno)
}

// landlockProbeError translates a failed ABI probe into an actionable
// error. Kept pure so tests can pin the wording without an old kernel.
func landlockProbeError(errno unix.Errno) error {
	switch {
	case errors.Is(errno, unix.ENOSYS):
		return errors.New("landlock unsupported: kernel does not implement Landlock (requires Linux 5.13+)")
	case errors.Is(errno, unix.EINVAL):
		return fmt.Errorf("landlock unsupported: kernel rejected the Landlock ABI probe: %w", errno)
	default:
		return fmt.Errorf("probe Landlock ABI: %w", errno)
	}
}

// landlockHandledAccess computes the filesystem rights this ruleset handles
// for the detected ABI. Bits unknown to the kernel make ruleset creation
// fail with EINVAL, so each addition is gated on the ABI version. The
// IOCTL_DEV bit (ABI 4) is deliberately NOT handled: bounding device ioctls
// is out of scope for development-shell containment and would break common
// terminal tooling.
func landlockHandledAccess(abi int) unix.LandlockRulesetAttr {
	handled := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return unix.LandlockRulesetAttr{Access_fs: handled}
}

// applyLandlock restricts the current thread tree to: read-write access
// under the workspace root and the resolved temp directory, plus read-
// execute access under the conventional system library/config trees that
// dynamically linked tooling (git, NSS, locale data) needs. Everything else
// loses filesystem access entirely.
func applyLandlock(workspaceRoot string) error {
	abi, err := landlockABIVersion()
	if err != nil {
		return fmt.Errorf("isolation: %w", err)
	}

	attr := landlockHandledAccess(abi)
	fd, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0, 0, 0, 0,
	)
	if errno != 0 {
		return fmt.Errorf("isolation: create Landlock ruleset: %w", errno)
	}
	defer unix.Close(int(fd))

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("isolation: resolve workspace %q: %w", workspaceRoot, err)
	}

	// Read-write: the workspace and the temp directory. When the temp
	// directory lives inside the workspace (common in sandboxes), one rule
	// suffices — a second rule on the same parent would overwrite the first.
	if err := addLandlockPathRule(int(fd), attr.Access_fs, root); err != nil {
		return err
	}
	tmp := os.TempDir()
	if !pathInside(tmp, root) {
		if err := addLandlockPathRule(int(fd), attr.Access_fs, tmp); err != nil {
			return err
		}
	}

	// Read-execute: system trees consumed by dynamically linked tooling.
	// Write access here stays denied even though the permission model may
	// allow broader writes — OS isolation is the second line of defense.
	readExec := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR)
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc"} {
		// Missing standard trees are normal on trimmed distros; skip silently.
		if err := addLandlockPathRule(int(fd), readExec, dir); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
	}

	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("isolation: enforce Landlock ruleset: %w", errno)
	}
	return nil
}

// addLandlockPathRule grants access under dir. Missing directories return a
// wrapped ENOENT so callers can decide whether absence is acceptable.
func addLandlockPathRule(rulesetFD int, access uint64, dir string) error {
	dirFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("isolation: open Landlock rule path %s: %w", dir, err)
	}
	defer unix.Close(dirFD)

	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(dirFD),
	}
	if _, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0,
	); errno != 0 {
		return fmt.Errorf("isolation: add Landlock rule for %s: %w", dir, errno)
	}
	return nil
}

// pathInside reports whether cleaned absolute path equals or lives under
// root. Lexicographic only — used for rule de-duplication, never as a
// security boundary (Landlock itself is).
func pathInside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// --- Seccomp ---------------------------------------------------------------

// Seccomp strategy (spec RNF-4.7): a conservative default-deny allowlist for
// spawned development tooling, reviewed against the container-default
// profiles (docker/moby default.json, libcontainer) plus the syscall needs
// of the Go runtime and typical build tools (go, git, make, compilers).
//
// How to extend: add the name to the matching group below AND confirm it
// exists for every supported architecture in go-seccomp-bpf's tables —
// assembly-time intersection below drops unknown names loudly in tests but
// silently in production, so new entries must be verified. Anything
// privilege-flavored stays out.
//
// Deliberately NOT allowed (each omission is a hardening decision):
//   - socket/connect/bind/listen/accept*/sendto/recvfrom/sendmsg/recvmsg/
//     socketpair/setsockopt/getsockopt: NO networking from shell children.
//     Network egress belongs to forge's adapter allowlist layer (RNF-4.9),
//     which a sandboxed shell cannot bypass.
//   - setuid/setgid/setgroups/setre*/setres*/setfsuid/setfsgid/capset:
//     privilege-change surface; shells run unprivileged already.
//   - ptrace/process_vm_readv/process_vm_writev: introspection attacks.
//   - mount/umount2/pivot_root/chroot/setns/unshare/fsopen/fsconfig/fsmount/
//     fspick/open_tree/move_mount/mount_setattr: container-escape surface.
//   - bpf/perf_event_open/userfaultfd/io_uring_*/kexec_*/init_module/
//     finit_module/delete_module/reboot/swapon/swapoff: kernel interfaces
//     with recurring CVE history; no dev tooling needs them.
//   - mknod/mknodat: device-node creation inside the sandbox.
//   - keyctl/add_key/request_key: kernel keyring manipulation.
//   - inotify_*/fanotify_*: unused by baseline tooling; shrinks surface.
//   - setsid: a session leader escapes this package's negative-pgid kill on
//     timeout, so session creation is denied by omission.
//   - personality/iopl/ioperm: ASLR and port-I/O tampering.
//   - SysV IPC (shmget/shmat/semget/msgget families): legacy, unused.
//
// Denied calls fail with EPERM (loud, debuggable) except clone3, which
// returns ENOSYS so glibc/musl fall back to clone exactly as container
// runtimes arrange (the moby default profile does the same).
func applySeccomp() error {
	filter := bpfFilter()
	if err := seccomp.LoadFilter(filter); err != nil {
		return fmt.Errorf("isolation: load seccomp filter: %w", err)
	}
	return nil
}

// bpfFilter builds the exact filter applySeccomp installs. Kept separate so
// tests can assemble it without installing anything.
func bpfFilter() seccomp.Filter {
	denyEPERM := seccompErrno(unix.EPERM)
	groups := allowedSyscallGroups(seccomp.ActionAllow)
	groups = append(groups, seccomp.SyscallGroup{
		Names:  []string{"clone3"},
		Action: seccompErrno(unix.ENOSYS),
	})
	return seccomp.Filter{
		NoNewPrivs: true, // idempotent: already set for Landlock
		Policy: seccomp.Policy{
			DefaultAction: denyEPERM,
			Syscalls:      groups,
		},
	}
}

// seccompErrno builds an Action that denies with the given errno.
func seccompErrno(errno unix.Errno) seccomp.Action {
	return seccomp.Action(uint32(seccomp.ActionErrno) | uint32(errno))
}

// baselineSyscallGroups returns the reviewable baseline split into logical
// groups, before architecture filtering.
func baselineSyscallGroups() [][]string {
	return [][]string{
		{
			// Base file I/O and memory: everything read-heavy tools need,
			// including the Go runtime's own mmap/futex/epoll usage.
			"read", "write", "readv", "writev",
			"open", "openat", "openat2", "close", "creat",
			"stat", "fstat", "lstat", "newfstatat", "statx",
			"lseek", "mmap", "munmap", "mprotect", "brk", "msync",
			"madvise", "mremap", "mincore", "memfd_create",
			"getcwd", "readlink", "readlinkat",
			"dup", "dup2", "dup3", "fcntl", "ioctl",
			"poll", "ppoll", "select", "pselect6",
			"pipe", "pipe2", "getdents", "getdents64",
			"faccessat", "faccessat2", "access",
			"chdir", "fchdir", "getrandom",
		},
		{
			// File mutation: create/rename/remove/metadata. Bounded in
			// scope by the Landlock ruleset, so allowing the calls here
			// cannot grant writes outside sanctioned trees.
			"rename", "renameat", "renameat2",
			"unlink", "unlinkat", "mkdir", "mkdirat", "rmdir",
			"symlink", "symlinkat", "link", "linkat",
			"truncate", "ftruncate",
			"utimensat", "futimesat", "utimes",
			"chmod", "fchmod", "fchmodat", "fchownat", "umask",
		},
		{
			// Filesystem metadata and bulk transfer used by build tooling.
			"statfs", "fstatfs", "fsync", "fdatasync", "fallocate",
			"flock", "sync", "syncfs",
			"sendfile", "copy_file_range", "splice", "vmsplice", "tee",
			"readahead",
		},
		{
			// Process lifecycle: fork/exec/wait and job control (setpgid
			// powers the daemon's process-group timeout kill). sched_* and
			// related getters are read-only introspection.
			"clone", "fork", "vfork", "execve", "execveat",
			"wait4", "waitid", "waitpid",
			"exit", "exit_group",
			"getpid", "getppid", "gettid",
			"kill", "tgkill",
			"set_tid_address", "set_robust_list", "get_robust_list", "rseq",
			"sched_getaffinity", "sched_yield",
			"sched_getparam", "sched_getscheduler",
			"sched_get_priority_max", "sched_get_priority_min",
			"getcpu", "setpgid", "getpgid", "getpgrp",
			"prctl", "arch_prctl", "membarrier",
		},
		{
			// Signals, time, and event loops.
			"rt_sigaction", "rt_sigprocmask", "rt_sigreturn",
			"rt_sigpending", "rt_sigtimedwait", "rt_sigsuspend",
			"sigaltstack", "restart_syscall", "pause",
			"futex",
			"clock_gettime", "clock_getres", "clock_nanosleep",
			"nanosleep", "gettimeofday", "time",
			"setitimer", "getitimer",
			"timerfd_create", "timerfd_settime", "timerfd_gettime",
			"eventfd", "eventfd2",
			"epoll_create", "epoll_create1", "epoll_ctl",
			"epoll_wait", "epoll_pwait", "epoll_pwait2",
		},
		{
			// Identity and limits: strictly read-only getters plus limit
			// adjustment within the caller's own bounds. No *set*uid/gid.
			"getuid", "getgid", "geteuid", "getegid", "getgroups",
			"getresuid", "getresgid",
			"getrlimit", "setrlimit", "prlimit64", "getrusage",
			"sysinfo", "uname",
		},
	}
}

// allowedSyscallGroups turns the baseline into seccomp syscall groups,
// dropping names absent from the running GOARCH's syscall table (legacy x86
// entries like stat/lstat/open/pipe on arm64). Groups emptied entirely by
// the intersection are skipped; the library ignores empty groups anyway.
func allowedSyscallGroups(allow seccomp.Action) []seccomp.SyscallGroup {
	baseline := baselineSyscallGroups()
	groups := make([]seccomp.SyscallGroup, 0, len(baseline)+1)
	for _, names := range baseline {
		names = intersectBaseline(names)
		if len(names) == 0 {
			continue
		}
		groups = append(groups, seccomp.SyscallGroup{Names: names, Action: allow})
	}
	return groups
}

// assembleFilterInstructionCount compiles the exact policy applySeccomp
// would load and reports how many BPF instructions it produced. Tests use
// this to prove assembly works without installing anything (and without
// importing the underlying BPF IR package directly).
func assembleFilterInstructionCount() (int, error) {
	policy := bpfFilter().Policy
	insts, err := policy.Assemble()
	if err != nil {
		return 0, err
	}
	return len(insts), nil
}

// intersectBaseline keeps only the names present in the compiled GOARCH's
// syscall table. If the table cannot be resolved, nothing is kept rather
// than pretending coverage exists.
func intersectBaseline(names []string) []string {
	info, err := arch.GetInfo("")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := info.SyscallNames[n]; ok {
			out = append(out, n)
		}
	}
	return out
}
