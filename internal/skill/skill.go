// Package skill implements the forge skills runtime: SKILL.md frontmatter parsing,
// validation, and semantic lazy-load via embeddings.
package skill

import "errors"

// Source identifies the origin of a skill.
type Source string

const (
	// SourceLocal indicates a skill created locally (no checksum required).
	SourceLocal Source = "local"
	// SourceExternal indicates a skill from an external source (checksum required).
	SourceExternal Source = "external"
)

// Origin identifies which root directory a skill was scanned from — not to
// be confused with Source (local/external, about provenance/trust). Origin
// is about scope: a project skill applies to this workspace only; a global
// skill (config.GlobalSkillsDir) applies to every project and, in manual
// activation mode (SkillsConfig.LazyLoad == false), is always active
// without needing to be listed in any project's config.
type Origin string

const (
	OriginProject Origin = "project"
	OriginGlobal  Origin = "global"
)

// Skill describes a single skill loaded from a SKILL.md file.
type Skill struct {
	// Name is the skill name; must match the directory basename and the
	// frontmatter name field.
	Name string
	// Category is an optional grouping label (e.g. "review", "testing").
	Category string
	// Description is a non-empty semantic description used for retrieval matching.
	Description string
	// Source indicates whether the skill is local or external.
	Source Source
	// Checksum is the expected sha256 for external skills (format "sha256:<64hex>").
	Checksum string
	// ActivationKeywords are optional keywords that improve retrieval matching.
	ActivationKeywords []string
	// Scripts are relative paths to reusable scripts declared in frontmatter.
	Scripts []string
	// Instructions is the markdown body after the frontmatter fences (verbatim).
	Instructions string
	// DirPath is the absolute or relative skill directory containing SKILL.md.
	DirPath string
	// Origin is which root this skill was scanned from (project or global).
	// Set by the Manager at scan time, not part of the SKILL.md frontmatter.
	Origin Origin
}

// Sentinel errors for the skill runtime.
var (
	// ErrInvalidSkill is a sentinel for skill validation failures.
	ErrInvalidSkill = errors.New("invalid skill")
	// ErrNotLoaded is returned when Enable/Disable references a skill that was not loaded.
	ErrNotLoaded = errors.New("skill not loaded")
	// ErrAlreadyEnabled is returned when Enable is called on an already-enabled skill.
	ErrAlreadyEnabled = errors.New("skill already enabled")
	// ErrNotEnabled is returned when Disable is called on a skill that is not enabled.
	ErrNotEnabled = errors.New("skill not enabled")
	// ErrChecksumMismatch is returned when an external skill's SHA256 does not match its manifest checksum.
	ErrChecksumMismatch = errors.New("skill checksum mismatch")
	// ErrApprovalRequired is returned when an external skill is scanned without explicit approval.
	ErrApprovalRequired = errors.New("external skill requires explicit approval")
)
