// Package perms implements forge's deny-by-default permission engine
// (spec RNF-4.1): every shell execution, filesystem access, and git
// operation is denied unless a configured rule explicitly allows it, and a
// non-configurable git safety floor denies destructive invocations before
// any allowlist is consulted (RNF-8.2 spirit). Forge's internal harness
// tools (kind "custom") invert the default — an explicit floor ALLOWS them
// because they never reach the host OS — while explicit deny rules still
// take precedence over that floor. The exception is the mutating subset of
// custom tools (anchoring_store, anchoring_delete): their effect is a
// persistent-memory write that reaches the context of every future turn,
// so for them the default flips back to DENY unless explicitly allowed
// (custom write floor, RNF-4.5/4.12).
//
// The engine is immutable after construction, so a single *Engine is safe
// for concurrent Check calls. All matching runs over forward-slash-normalized
// paths via internal/pathmatch; glob semantics live there and are shared
// with configuration validation.
package perms

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/pathmatch"
)

// Kind enumerates the operation classes the engine arbitrates.
type Kind string

const (
	// KindFsRead is a filesystem read request.
	KindFsRead Kind = "fs.read"
	// KindFsWrite is a filesystem write request.
	KindFsWrite Kind = "fs.write"
	// KindShell is a shell command execution request.
	KindShell Kind = "shell.exec"
	// KindGit is a git invocation request.
	KindGit Kind = "git"
	// KindCustom is a forge-internal harness tool request (the v1 tools:
	// retrieval_search, compaction_summarize, anchoring_*). These tools
	// only touch forge's own SQLite database and forge's own LLM client,
	// never the host OS, so they sit inside the trust boundary the engine
	// guards; see the custom floor in evaluate for why they are allowed
	// by default. The mutating subset (anchoring_store, anchoring_delete)
	// is the exception: it is denied by default by the custom write floor.
	KindCustom Kind = "custom"
)

// Request describes one operation seeking authorization. Only the fields
// relevant to Kind are consulted; others are ignored.
type Request struct {
	Kind       Kind
	Path       string   // fs.*: path as given (relative or absolute)
	Command    string   // shell.exec: executable name or path; custom: tool name (audit trail identifier)
	Args       []string // shell.exec: command arguments (never affect the decision)
	Subcommand string   // git: e.g. "push"
	GitArgs    []string // git: arguments after the subcommand

	// Tool execution parameters (not used by permission engine)
	Offset     int64  // fs.read: byte offset to start reading from
	Limit      int64  // fs.read: max bytes to read (-1 = all)
	Content    string // fs.write: content to write
	Encoding   string // fs.write: "utf8" or "base64"
	CreateDirs bool   // fs.write: create parent directories
	Recursive  bool   // fs.list: recursive listing
	Pattern    string // fs.list: doublestar glob pattern
	TimeoutSec int    // shell.exec: timeout in seconds (default 120, max 300)
	Workdir    string // shell.exec, git: working directory

	// Input carries the raw, already-schema-validated tool arguments for
	// the v1 custom tools (KindCustom), which read their structured
	// parameters from here. It is populated by tools.Registry.Execute and
	// is never consulted by the permission engine or the audit trail.
	// Deliberately NOT Args: that field is argv-shaped and stays
	// shell.exec-only, so the two channels can never be confused.
	Input map[string]any
}

// Decision is the outcome of one permission check.
type Decision struct {
	Allowed bool
	// Rule names what decided: "malformed-request", "git-floor",
	// "floor:custom", "custom-write-floor:<tool>", "default-deny:<kind>",
	// or an allowing rule "<kind>:<pattern>" (fs), "<kind>:<basename>"
	// (shell), "<kind>:<subcommand>" (git); a denying custom rule is
	// "custom:<tool>".
	Rule string
}

// FSPermissions bounds filesystem access with glob patterns matched against
// workspace-relative paths (relative patterns) or absolute paths (absolute
// patterns — the documented escape hatch for paths outside the workspace).
type FSPermissions struct {
	Read  []string `json:"read"`
	Write []string `json:"write"`
}

// ShellPermissions allows shell executables by base name (case-insensitive).
// Entries without glob meta (*, ?, [...]) match exactly via EqualFold.
// Entries containing glob meta match the base name case-insensitively as a
// glob (see shell matching in evaluate). An entry of "*" matches any
// executable and effectively disables deny-by-default for shell; this is
// intentional and was explicitly requested by the owner.
type ShellPermissions struct {
	Allow []string `json:"allow"`
}

// GitPermissions allows git subcommands (lowercase convention; uppercase is
// never matched). Destructive subcommands stay blocked by the safety floor
// regardless of this list.
type GitPermissions struct {
	Allow []string `json:"allow"`
}

// CustomPermissions arbitrates forge-internal harness tools (kind "custom")
// by tool name, case-sensitively (the tools.BuildPermsRequest names are
// fixed lowercase-underscore strings). Unlike the OS-reaching kinds, the
// default for non-mutating tools is ALLOW — the custom floor — because they
// never reach the host OS; an explicit deny entry is the way to turn one off.
//
// The mutating subset (anchoring_store, anchoring_delete — see
// customMutatingTools) inverts that default: the custom write floor DENIES
// it unless the tool is named in Allow. Semantics of the two lists:
//
//   - Deny turns any custom tool off, mutating or not.
//   - Allow restores one mutating tool explicitly; on a non-mutating (read)
//     tool an allow entry is a NO-OP — reads are already floor-allowed and
//     their decision rule stays "floor:custom".
//   - When a tool appears in both lists, DENY wins (fail-closed): deny rules
//     are evaluated before the allow list, mirroring how the git floor
//     precedes the git allowlist.
type CustomPermissions struct {
	Deny  []string `json:"deny"`
	Allow []string `json:"allow"`
}

// customMutatingTools is the set of custom tool names whose effect is a
// persistent-memory mutation: they write or destroy content that reaches
// the context of every future turn. The custom floor's original rationale —
// "these tools never reach the host OS" — does not cover them: the
// persistence boundary they cross is the model-facing one (RNF-4.5), and a
// model-proposed anchor must never be anchored automatically (RNF-4.12).
// Membership is the authority for the write floor in evaluate; adding a
// future mutating tool means adding it here AND shipping an explicit allow
// path for the owner.
var customMutatingTools = map[string]struct{}{
	"anchoring_store":  {},
	"anchoring_delete": {},
}

// isCustomMutatingTool reports whether name is a persistent-memory-mutating
// custom tool (see customMutatingTools).
func isCustomMutatingTool(name string) bool {
	_, ok := customMutatingTools[name]
	return ok
}

// PermissionsPolicy mirrors the config document's "permissions" section.
// It is deny-by-default: anything not explicitly allowed is refused by the
// permission engine (RNF-4.1). The one exception is the non-mutating subset
// of the "custom" kind, which the custom floor allows by default (see
// CustomPermissions); mutating custom tools stay deny-by-default behind the
// custom write floor.
type PermissionsPolicy struct {
	FS     FSPermissions     `json:"fs"`
	Shell  ShellPermissions  `json:"shell"`
	Git    GitPermissions    `json:"git"`
	Custom CustomPermissions `json:"custom"`
}

// patternLists holds one fs pattern list split into its relative and absolute
// halves at construction time so Check never re-classifies patterns.
type patternLists struct {
	relative []string
	absolute []string
}

// splitPatterns partitions patterns by IsAbsolute. All entries must already
// be validated.
func splitPatterns(patterns []string) patternLists {
	var out patternLists
	for _, p := range patterns {
		if pathmatch.IsAbsolute(p) {
			out.absolute = append(out.absolute, p)
		} else {
			out.relative = append(out.relative, p)
		}
	}
	return out
}

// Engine is an immutable permission evaluator: construct once with New,
// then call Check from any number of goroutines.
type Engine struct {
	fsRead  patternLists
	fsWrite patternLists

	shellAllow  []string
	gitAllow    []string
	customDeny  []string
	customAllow []string

	workspaceRoot string // cleaned absolute path
	logger        *slog.Logger
}

// New validates the whole policy and constructs an Engine. Validation is
// eager and total: any invalid pattern or empty allowlist entry is an error
// here, never a silent mismatch later. workspaceRoot must already be an
// absolute path (callers resolve "~" and relative forms beforehand); logger
// is optional and nil disables audit logging.
func New(policy PermissionsPolicy, workspaceRoot string, logger *slog.Logger) (*Engine, error) {
	if !filepath.IsAbs(workspaceRoot) {
		return nil, fmt.Errorf("workspace root %q must be an absolute path (resolve it before constructing the engine)", workspaceRoot)
	}
	if err := validatePatternList("permissions.fs.read", policy.FS.Read); err != nil {
		return nil, err
	}
	if err := validatePatternList("permissions.fs.write", policy.FS.Write); err != nil {
		return nil, err
	}
	if err := validateShellAllowList("permissions.shell.allow", policy.Shell.Allow); err != nil {
		return nil, err
	}
	if err := validateAllowList("permissions.git.allow", policy.Git.Allow); err != nil {
		return nil, err
	}
	if err := validateAllowList("permissions.custom.deny", policy.Custom.Deny); err != nil {
		return nil, err
	}
	if err := validateAllowList("permissions.custom.allow", policy.Custom.Allow); err != nil {
		return nil, err
	}
	return &Engine{
		fsRead:        splitPatterns(policy.FS.Read),
		fsWrite:       splitPatterns(policy.FS.Write),
		shellAllow:    policy.Shell.Allow,
		gitAllow:      policy.Git.Allow,
		customDeny:    policy.Custom.Deny,
		customAllow:   policy.Custom.Allow,
		workspaceRoot: filepath.Clean(workspaceRoot),
		logger:        logger,
	}, nil
}

// validatePatternList checks every fs glob through the shared matcher's
// validator, wrapping errors with section context for actionable messages.
func validatePatternList(section string, patterns []string) error {
	for i, p := range patterns {
		if err := pathmatch.ValidatePattern(p); err != nil {
			return fmt.Errorf("%s[%d] %q: %w", section, i, p, err)
		}
	}
	return nil
}

// validateAllowList rejects empty allowlist entries, which could otherwise
// never match and would silently narrow the policy.
func validateAllowList(section string, entries []string) error {
	for i, e := range entries {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%s[%d]: entries must be non-empty", section, i)
		}
	}
	return nil
}

// validateShellAllowList validates shell.allow entries: non-empty and, when
// glob meta is present, syntactically valid as a glob (same style as fs
// pattern validation, eagerly at construction time).
func validateShellAllowList(section string, entries []string) error {
	for i, e := range entries {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("%s[%d]: entries must be non-empty", section, i)
		}
		if containsShellGlobMeta(e) {
			if _, err := filepath.Match(strings.ToLower(e), "a"); err != nil {
				return fmt.Errorf("%s[%d] %q: %w", section, i, e, err)
			}
		}
	}
	return nil
}

// containsShellGlobMeta reports whether s contains glob meta that switches it
// to pattern matching. Kept deliberately tiny: *, ?, or '[' suffices to
// identify the glob form the owner asked for.
func containsShellGlobMeta(s string) bool {
	return strings.Contains(s, "*") || strings.Contains(s, "?") || strings.Contains(s, "[")
}

// shellGlobMatches reports whether the glob pattern matches the base name
// case-insensitively. Matcher choice: filepath.Match with lowercased inputs
// is used because pathmatch only implements '*' within a single segment and
// cannot handle '?' or '[...]'; lowercasing gives the same case-insensitive
// behavior as the exact EqualFold path.
func shellGlobMatches(pattern, base string) bool {
	matched, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(base))
	if err != nil {
		return false
	}
	return matched
}

// Check evaluates req against the policy and returns the decision. Exactly
// one audit record ("perm.check") is emitted per call when a logger is
// attached. Evaluation order, first match wins:
//
//  1. Malformed requests are denied outright ("malformed-request").
//  2. git only: the safety floor denies destructive invocations ("git-floor")
//     BEFORE the allowlist is consulted.
//  3. The kind's list is matched; a hit allows ("<kind>:<pattern>" for fs,
//     "<kind>:<basename>" for shell, "<kind>:<subcommand>" for git) or, for
//     custom, denies ("custom:<tool>") or explicitly allows a mutating tool
//     ("custom:<tool>").
//  4. custom only: the custom floor ALLOWS non-mutating internal harness
//     tools ("floor:custom") when no deny rule matched; the custom write
//     floor DENIES mutating tools ("custom-write-floor:<tool>") unless an
//     explicit allow entry matched in step 3.
//  5. Otherwise default deny ("default-deny:<kind>").
func (e *Engine) Check(req Request) Decision {
	d := e.evaluate(req)
	e.audit(req, d)
	return d
}

// evaluate applies the four-step order without logging.
func (e *Engine) evaluate(req Request) Decision {
	if malformedRequest(req) {
		return Decision{Allowed: false, Rule: "malformed-request"}
	}

	switch req.Kind {
	case KindFsRead:
		if pat, ok := e.matchFS(req.Path, e.fsRead); ok {
			return Decision{Allowed: true, Rule: string(KindFsRead) + ":" + pat}
		}
	case KindFsWrite:
		if pat, ok := e.matchFS(req.Path, e.fsWrite); ok {
			return Decision{Allowed: true, Rule: string(KindFsWrite) + ":" + pat}
		}
	case KindShell:
		base := commandBase(req.Command)
		for _, allowed := range e.shellAllow {
			// Base-name comparison is case-insensitive on ALL platforms:
			// Windows filenames are case-preserving-insensitive and POSIX
			// builds prefer predictability over pedantry here. When the
			// allow entry contains glob meta (*, ?, [...]) it is matched as
			// a case-insensitive glob against the base name; "*" alone thus
			// matches any executable and effectively disables deny-by-default
			// for shell (owner explicitly requested this escape hatch).
			if containsShellGlobMeta(allowed) {
				if shellGlobMatches(allowed, base) {
					return Decision{Allowed: true, Rule: string(KindShell) + ":" + allowed}
				}
				continue
			}
			if strings.EqualFold(base, allowed) {
				return Decision{Allowed: true, Rule: string(KindShell) + ":" + allowed}
			}
		}
	case KindGit:
		// Floor first: no configuration can authorize what it forbids.
		if IsDestructiveGit(req.Subcommand, req.GitArgs) {
			return Decision{Allowed: false, Rule: "git-floor"}
		}
		for _, allowed := range e.gitAllow {
			// Case-SENSITIVE by convention: git subcommands are lowercase;
			// "COMMIT" is not a recognized spelling and stays unmatched.
			if req.Subcommand == allowed {
				return Decision{Allowed: true, Rule: string(KindGit) + ":" + allowed}
			}
		}
	case KindCustom:
		// Custom floor (the allow-side mirror of the git floor's
		// "floor decides" idea): explicit deny rules take precedence;
		// when no rule matches, ALLOW. These are internal harness tools
		// that only touch forge's own SQLite DB and forge's own LLM
		// client — they never cross the OS boundary that fs/shell/git
		// guard, so they sit inside the trust boundary, and the
		// deny-by-default invariant stays intact for OS-reaching
		// operations.
		for _, denied := range e.customDeny {
			if req.Command == denied {
				return Decision{Allowed: false, Rule: string(KindCustom) + ":" + denied}
			}
		}
		// Custom write floor: mutating tools persist or destroy content
		// that reaches the context of every future turn (RNF-4.5/4.12),
		// so the "never reaches the host OS" rationale does not apply to
		// them. Their default flips to DENY unless an explicit allow entry
		// restores them; because deny rules were already checked above,
		// an allow+deny conflict resolves to DENY (fail-closed).
		if isCustomMutatingTool(req.Command) {
			for _, allowed := range e.customAllow {
				if req.Command == allowed {
					return Decision{Allowed: true, Rule: string(KindCustom) + ":" + allowed}
				}
			}
			return Decision{Allowed: false, Rule: "custom-write-floor:" + req.Command}
		}
		return Decision{Allowed: true, Rule: "floor:custom"}
	}

	return Decision{Allowed: false, Rule: "default-deny:" + string(req.Kind)}
}

// malformedRequest reports whether req is too incomplete to evaluate.
// Unknown kinds are malformed rather than silently denied-with-a-rule, so
// future kinds fail loudly during rollout.
func malformedRequest(req Request) bool {
	switch req.Kind {
	case KindFsRead, KindFsWrite:
		return req.Path == ""
	case KindShell:
		return req.Command == ""
	case KindGit:
		return req.Subcommand == ""
	case KindCustom:
		return req.Command == ""
	default:
		return true // empty or unknown kind
	}
}

// commandBase extracts the case-preserving base name of a command given as
// bare name or path with either separator style.
func commandBase(command string) string {
	normalized := strings.ReplaceAll(command, "\\", "/")
	if i := strings.LastIndexByte(normalized, '/'); i >= 0 {
		return normalized[i+1:]
	}
	return normalized
}

// matchFS matches reqPath (as given) against one fs pattern list. The path
// is resolved lexically (filepath.Clean+Abs); symlinks are deliberately NOT
// followed — the request is judged by the path it claims, and resolving
// aliases is the OS isolation layer's job (RNF-4.7).
//
// Relative patterns are tested against the workspace-relative cleaned path;
// absolute patterns against the forward-slashed absolute path. A request
// that escapes the workspace (its relative form starts with ".." or cannot
// be computed, e.g. another drive on Windows) auto-denies UNLESS an absolute
// pattern matches it — that is the documented escape hatch for explicitly
// authorized out-of-workspace locations.
func (e *Engine) matchFS(reqPath string, lists patternLists) (matched string, ok bool) {
	abs, err := filepath.Abs(reqPath)
	if err != nil {
		return "", false
	}
	rel, inside := e.workspaceRel(abs)
	if inside {
		for _, p := range lists.relative {
			if pathmatch.Match(p, rel) {
				return p, true
			}
		}
	}
	absSlash := filepath.ToSlash(abs)
	for _, p := range lists.absolute {
		if pathmatch.Match(p, absSlash) {
			return p, true
		}
	}
	return "", false
}

// workspaceRel returns the forward-slashed workspace-relative form of abs
// and whether abs lies inside the workspace at all. Escaped or incomputable
// cases return the forward-slashed absolute path with inside=false.
func (e *Engine) workspaceRel(abs string) (rel string, inside bool) {
	r, err := filepath.Rel(e.workspaceRoot, abs)
	if err != nil {
		return filepath.ToSlash(abs), false
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) || strings.HasPrefix(r, "../") {
		return filepath.ToSlash(abs), false
	}
	return filepath.ToSlash(r), true
}
