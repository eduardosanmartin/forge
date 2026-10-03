package perms

import (
	"path/filepath"
	"runtime"
	"strings"
)

// This file implements the shell floor: invariants checked BEFORE the
// shell allowlist, mirroring the git floor (gitfloor.go). The allowlist
// decides WHICH programs may run; the floor closes ways an allowed program
// name could be abused to escape the policy:
//
//   - A path-qualified command that resolves INSIDE the workspace is a file
//     the agent may have written itself (fs.write), so "go" in the allowlist
//     must not authorize "./src/go" (which, on Windows, exec resolves to
//     ./src/go.bat). Such a command runs only when an allow entry names that
//     exact path. Path-qualified commands outside the workspace
//     ("/usr/local/bin/go") keep matching by base name, as before.
//   - git invoked through the shell is subject to the same non-configurable
//     git floor (RNF-8.2) as the git tool — otherwise allowing "git" in
//     shell.allow would bypass it ("git push --force").
//   - The working directory must stay inside the workspace (shell and git
//     tools previously accepted any existing directory on the machine).
func (e *Engine) shellFloor(req Request) (Decision, bool) {
	if req.Workdir != "" && !e.workdirInside(req.Workdir) {
		return Decision{Allowed: false, Rule: "workdir-outside-workspace"}, true
	}

	if strings.ContainsAny(req.Command, programPathSeps) {
		base := req.Workdir
		if base == "" {
			base = e.workspaceRoot
		} else if !filepath.IsAbs(base) {
			base = filepath.Join(e.workspaceRoot, base)
		}
		target := req.Command
		switch {
		case filepath.IsAbs(target):
		case strings.HasPrefix(target, "/") || strings.HasPrefix(target, `\`):
			// Rooted but drive-less on Windows ("/usr/bin/go"): anchored
			// at the current drive's root, not at the workspace.
			if abs, err := filepath.Abs(target); err == nil {
				target = abs
			}
		default:
			target = filepath.Join(base, target)
		}
		if rel, inside := e.workspaceRel(filepath.Clean(target)); inside && !e.shellAllowsExactPath(rel) {
			return Decision{Allowed: false, Rule: "shell-workspace-executable"}, true
		}
	}

	if isGitProgram(commandBase(req.Command)) {
		sub, rest := gitSubcommand(req.Args)
		if IsDestructiveGit(sub, rest) {
			return Decision{Allowed: false, Rule: "git-floor"}, true
		}
	}
	return Decision{}, false
}

// programPathSeps are the characters that make a command a path to a
// program rather than a name looked up in PATH: "/" everywhere, and "\"
// only on Windows (on Unix "\" is an ordinary filename character).
var programPathSeps = defaultProgramPathSeps()

func defaultProgramPathSeps() string {
	if runtime.GOOS == "windows" {
		return `/\`
	}
	return "/"
}

// workdirInside reports whether dir (relative paths resolve against the
// workspace root) is the workspace root or below it.
func (e *Engine) workdirInside(dir string) bool {
	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.workspaceRoot, abs)
	}
	_, inside := e.workspaceRel(filepath.Clean(abs))
	return inside
}

// shellAllowsExactPath reports whether some shell allow entry names the
// workspace-relative program path rel exactly ("./scripts/check.sh" or
// "scripts/check.sh", either slash direction, case-insensitively).
func (e *Engine) shellAllowsExactPath(rel string) bool {
	want := normalizeProgramPath(rel)
	for _, allowed := range e.shellAllow {
		cmdPat, _, _ := splitShellEntry(allowed)
		if !strings.ContainsAny(cmdPat, `/\`) {
			continue
		}
		if strings.EqualFold(normalizeProgramPath(cmdPat), want) {
			return true
		}
	}
	return false
}

func normalizeProgramPath(p string) string {
	p = filepath.ToSlash(p)
	return strings.TrimPrefix(p, "./")
}

// splitShellEntry splits a shell allow entry into its program part and an
// optional argument pattern ("go test *" -> "go", "test *", true).
func splitShellEntry(entry string) (cmdPat, argPat string, hasArgPat bool) {
	entry = strings.TrimSpace(entry)
	i := strings.IndexAny(entry, " \t")
	if i < 0 {
		return entry, "", false
	}
	return entry[:i], strings.TrimSpace(entry[i+1:]), true
}

// wildcardMatch matches s against pattern where "*" matches any run of
// characters (spaces and slashes included — arguments are free text, not
// paths) and "?" matches exactly one character. Case-sensitive: argument
// values usually are.
func wildcardMatch(pattern, s string) bool {
	p, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// isGitProgram reports whether a command base name is git ("git",
// "git.exe", any case).
func isGitProgram(base string) bool {
	b := strings.ToLower(base)
	return b == "git" || b == "git.exe"
}

// gitSubcommand extracts the subcommand from a raw git argv, skipping git's
// global options ("-C <dir>", "-c <k=v>", "--git-dir=...", "--no-pager"...)
// that may precede it, and returns the arguments after it.
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--config-env":
			i++ // global option whose value is the next argument
		case strings.HasPrefix(a, "-"):
			// flag or --opt=value form
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}
