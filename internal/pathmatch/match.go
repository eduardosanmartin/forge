// Package pathmatch implements forge's hand-rolled doublestar glob matcher
// for permission patterns. It owns the glob semantics for both the permission
// engine (internal/perms) and configuration validation (internal/config),
// keeping the two in lockstep without import cycles.
//
// Semantics:
//
//   - Paths and patterns are expressed with forward slashes, always.
//   - Segments are obtained by splitting on "/".
//   - "**" as a WHOLE segment matches one or more preceding-context segments;
//     see the trailing-rule note below. Elsewhere (middle of a pattern) it
//     matches zero or more whole segments.
//   - "*" matches any run of characters within a single segment (never "/").
//     "**" is only special as a standalone segment; "a**b" degrades to "a*b".
//   - Everything else is a literal, case-sensitive segment match.
//   - A leading "./" on patterns is stripped.
//   - The empty pattern never matches anything.
//   - A trailing "/" on a pattern means directory-prefix semantics and is
//     exactly equivalent to appending "**".
//
// Trailing "**" rule (deliberate deviation from naive zero-or-more):
// "dir/**" matches everything strictly INSIDE dir ("dir/a", "dir/a/b/c"),
// but NOT "dir" itself. A bare "**" pattern (nothing before it) matches
// everything, including top-level entries. This decision is pinned by tests.
//
// The package has zero dependencies outside the standard library.
package pathmatch

import (
	"errors"
	"strings"
)

// maxDrivePrefixLen is the longest form treated as a drive-letter absolute
// prefix ("C:/" or bare "C:" at the very start of a pattern).
const maxDrivePrefixLen = 3

// ValidatePattern reports whether p is a syntactically valid permission
// pattern. It rejects:
//
//   - the empty pattern,
//   - any backslash (patterns must use "/" separators),
//   - any ".." segment (permission patterns may never traverse upward),
//   - empty segments ("a//b") and stray "." segments,
//   - patterns reduced to nothing after normalization ("/" alone, "./" alone).
//
// A pattern is ABSOLUTE when it starts with "/" (POSIX form) or with a
// drive-letter prefix in forward-slash form ("C:/...", normalized
// internally). Every other pattern is relative and must not pretend to be
// rooted: rooted-looking relative input is impossible because a leading "/"
// classifies the pattern as absolute by definition.
func ValidatePattern(p string) error {
	if p == "" {
		return errors.New("pattern is empty")
	}
	if strings.Contains(p, `\`) {
		return errors.New(`pattern contains a backslash; use "/" separators`)
	}
	segs, ok := patternSegments(p)
	if !ok {
		return errors.New(`pattern reduces to no segments (bare "/" or "./")`)
	}
	for _, s := range segs {
		switch s {
		case "":
			return errors.New(`pattern contains an empty segment (did you mean "a/b"?)`)
		case ".":
			return errors.New(`pattern contains a "." segment; drop it or use a "./" prefix`)
		case "..":
			return errors.New(`pattern contains a ".." segment; upward traversal is never allowed`)
		}
	}
	return nil
}

// IsAbsolute reports whether p is an absolute pattern: POSIX-rooted
// (leading "/") or drive-letter rooted ("C:/..." or bare "C:") in
// forward-slash form. Classification drives which request paths a pattern
// is tested against (workspace-relative vs absolute).
func IsAbsolute(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	// Drive-letter form: single ASCII letter, colon, then "/" or end.
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		if len(p) == 2 {
			return true
		}
		if len(p) >= maxDrivePrefixLen && p[2] == '/' {
			return true
		}
	}
	return false
}

// Match reports whether the forward-slashed path is matched by the pattern.
// Both inputs are expected in forward-slash form; the pattern goes through
// the same normalization as ValidatePattern (stripping "./", expanding a
// trailing "/" into "/**"). An invalid or empty pattern never matches.
// The path is matched verbatim: callers resolve it to the right basis
// (workspace-relative or absolute) beforehand.
func Match(pattern, path string) bool {
	if pattern == "" || path == "" {
		return false
	}
	patSegs, ok := patternSegments(pattern)
	if !ok {
		return false
	}
	pathSegs := strings.Split(strings.TrimPrefix(path, "./"), "/")
	for _, s := range pathSegs {
		if s == ".." {
			// Defensive: matcher never reasons across upward traversal;
			// callers decide escape policy before reaching here.
			return false
		}
	}
	return matchSegs(patSegs, 0, pathSegs, 0)
}

// patternSegments normalizes a pattern into its segments. It strips leading
// "./" prefixes, drops a single leading "/" (the POSIX root marker — a second
// consecutive slash survives and is rejected as an empty segment later),
// expands a trailing "/" into a final "**" segment, and reports ok=false for
// inputs that reduce to nothing.
func patternSegments(p string) ([]string, bool) {
	work := p
	for strings.HasPrefix(work, "./") {
		work = work[2:]
	}
	if strings.HasPrefix(work, "/") {
		work = work[1:]
	}
	trailingDir := strings.HasSuffix(work, "/")
	work = strings.TrimSuffix(work, "/")
	if work == "" {
		// Covers "/" alone and "./" alone; a genuine empty pattern is
		// rejected earlier by ValidatePattern and never matches in Match.
		return nil, false
	}
	segs := strings.Split(work, "/")
	if trailingDir {
		segs = append(segs, "**")
	}
	return segs, true
}

// matchSegs recursively matches pattern segments [pi:] against path segments
// [sj:]. Enforces the trailing-"**" rule: a "**" that ends the pattern AND
// follows at least one segment must consume at least one path segment, so
// "dir/**" does not match "dir" itself. A leading bare "**" keeps classic
// zero-or-more behavior.
func matchSegs(pat []string, pi int, path []string, pj int) bool {
	if pi == len(pat) {
		return pj == len(path)
	}
	if pat[pi] == "**" {
		start := pj
		if pi == len(pat)-1 && pi > 0 {
			start = pj + 1 // trailing "dir/**": must descend inside dir
		}
		for k := start; k <= len(path); k++ {
			if matchSegs(pat, pi+1, path, k) {
				return true
			}
		}
		return false
	}
	if pj == len(path) {
		return false
	}
	if matchSegment(pat[pi], path[pj]) {
		return matchSegs(pat, pi+1, path, pj+1)
	}
	return false
}

// matchSegment reports whether s matches a single pattern segment in which
// only "*" is special (any run of characters, never crossing "/").
// Comparison is case-sensitive byte-wise.
func matchSegment(pattern, s string) bool {
	pi, si := 0, 0
	starIdx, backtrack := -1, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && pattern[pi] == '*':
			starIdx = pi
			backtrack = si
			pi++
		case pi < len(pattern) && pattern[pi] == s[si]:
			pi++
			si++
		case starIdx != -1:
			backtrack++
			si = backtrack
			pi = starIdx + 1
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// isASCIILetter reports whether b is an ASCII letter (drive-letter check).
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
