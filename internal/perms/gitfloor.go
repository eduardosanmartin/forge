package perms

import "strings"

// This file implements forge's git safety floor: a fixed set of destructive
// git invocations that is denied BEFORE any configured allowlist is consulted
// (evaluation order step 2 in Check). It is the v0, in-process mirror of the
// non-configurable floor demanded by RNF-8.2 ("operaciones git destructivas:
// force-push, reset --hard, borrado de branch") and honors RNF-4.7's spirit:
// the permission model declares what is authorized; this floor guarantees a
// minimal invariant even if configuration is permissive by mistake.
//
// The floor is intentionally MINIMAL and extensible: later versions may add
// cases (e.g. filter-branch, reflog expire) but v0 keeps exactly the
// operations named above.

// IsDestructiveGit reports whether the git invocation (subcommand plus args)
// crosses the safety floor and must be denied regardless of configuration.
//
// Rules (v0):
//
//   - push with --force or -f: denied. Short-flag clusters count when they
//     CONTAIN the force letter ("-ff", "-fv"). Long-flag comparison is
//     case-insensitive ("--FORCE" is caught). "--force-with-lease" is NOT a
//     floor violation: it refuses non-fast-forward updates unless the remote
//     still matches the caller's expectation, which preserves the safety
//     property the floor exists to protect.
//   - reset with --hard anywhere in args: denied (flag position irrelevant).
//   - clean: ALWAYS denied in v0, including dry-run variants ("-n"): it
//     destroys untracked files and there is no conservative-enough subset to
//     expose before OS-level isolation matures (RNF-4.7).
//   - branch with -D or --delete: denied (forced deletion). Plain "-d" stays
//     allowed: git itself refuses it unless the branch is fully merged.
//     Because "-d" and "-D" carry DIFFERENT semantics, short-letter matching
//     for this rule is case-SENSITIVE; only an uppercase "D" triggers.
//
// The subcommand is compared case-insensitively so the floor cannot be
// bypassed with unusual casing; the allowlist (not the floor) enforces the
// lowercase convention elsewhere.
func IsDestructiveGit(sub string, args []string) bool {
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "push":
		return hasLongFlag(args, "--force") || shortClusterContainsFold(args, 'f')
	case "reset":
		return hasLongFlag(args, "--hard")
	case "clean":
		return true
	case "branch":
		return hasLongFlag(args, "--delete") || shortClusterContainsUpper(args, 'D')
	default:
		return false
	}
}

// hasLongFlag reports whether args contain want as an exact token,
// case-insensitively (so "--FORCE" matches "--force").
func hasLongFlag(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}

// isShortCluster reports whether arg is a single-dash flag cluster
// ("-abc"), i.e. starts with exactly one dash.
func isShortCluster(arg string) bool {
	return len(arg) >= 2 && arg[0] == '-' && arg[1] != '-'
}

// shortClusterContainsFold reports whether any single-dash cluster contains
// letter, case-insensitively. Used where the letter has no conflicting
// uppercase sibling meaning; deny-biased on purpose ("-F" for push is not a
// real git flag, so catching costs nothing legitimate).
func shortClusterContainsFold(args []string, letter byte) bool {
	lower := lowerByte(letter)
	for _, a := range args {
		if !isShortCluster(a) {
			continue // long flags handled separately; keeps "--force-with-lease" safe
		}
		for i := 1; i < len(a); i++ {
			if lowerByte(a[i]) == lower {
				return true
			}
		}
	}
	return false
}

// shortClusterContainsUpper reports whether any single-dash cluster contains
// the UPPERCASE letter exactly. Case-sensitive by design: for "branch",
// lowercase "-d" (delete merged) is safe while uppercase "-D" (forced
// delete) is not, so folding case would forbid a legitimate operation.
func shortClusterContainsUpper(args []string, letter byte) bool {
	for _, a := range args {
		if !isShortCluster(a) {
			continue
		}
		if strings.IndexByte(a[1:], letter) >= 0 {
			return true
		}
	}
	return false
}

// lowerByte lowercases a single ASCII byte without importing unicode for a
// two-call site helper.
func lowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
