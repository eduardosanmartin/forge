package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// validateSkill validates s according to spec rules.
// It aggregates all violations with errors.Join.
func validateSkill(s *Skill) error {
	var errs []error

	nameRe := regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	if s.Name == "" {
		errs = append(errs, errors.New("name: must not be empty"))
	} else if !nameRe.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf("name %q: must match ^[a-z][a-z0-9_-]{1,63}$ (lowercase, digits, hyphen, underscore, 2-64 chars)", s.Name))
	}

	if strings.TrimSpace(s.Description) == "" {
		errs = append(errs, errors.New("description: must not be empty"))
	}

	if s.Source != SourceLocal && s.Source != SourceExternal {
		errs = append(errs, fmt.Errorf("source %q: must be %q or %q", s.Source, SourceLocal, SourceExternal))
	}

	checksumRe := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	if s.Source == SourceExternal {
		if s.Checksum == "" {
			errs = append(errs, errors.New("checksum: required when source is \"external\""))
		} else if !checksumRe.MatchString(s.Checksum) {
			errs = append(errs, fmt.Errorf("checksum %q: must match ^sha256:[0-9a-f]{64}$", s.Checksum))
		}
	} else if s.Source == SourceLocal {
		if s.Checksum != "" {
			errs = append(errs, errors.New("checksum: must not be set when source is \"local\" (only for external)"))
		}
	} else {
		if s.Checksum != "" && !checksumRe.MatchString(s.Checksum) {
			errs = append(errs, fmt.Errorf("checksum %q: must match ^sha256:[0-9a-f]{64}$", s.Checksum))
		}
	}

	// Dir basename must equal frontmatter name.
	if s.DirPath != "" && s.Name != "" {
		base := filepath.Base(filepath.Clean(s.DirPath))
		if base != s.Name {
			errs = append(errs, fmt.Errorf("directory name %q must equal frontmatter name %q", base, s.Name))
		}
	}

	// Scripts: each path relative (no absolute, no ".."), file must exist.
	for i, p := range s.Scripts {
		if isAbsolutePath(p) {
			errs = append(errs, fmt.Errorf("scripts[%d] %q: absolute paths are not allowed", i, p))
			continue
		}
		normalized := strings.ReplaceAll(p, "\\", "/")
		segments := strings.Split(normalized, "/")
		for _, seg := range segments {
			if seg == ".." {
				errs = append(errs, fmt.Errorf("scripts[%d] %q: must not contain \"..\"", i, p))
				break
			}
		}
		if p == "" {
			errs = append(errs, fmt.Errorf("scripts[%d]: must not be empty", i))
			continue
		}
		// Existence check inside skill dir.
		if s.DirPath != "" {
			// Resolve script path relative to skill dir.
			fullPath := filepath.Join(s.DirPath, filepath.FromSlash(p))
			if _, err := os.Stat(fullPath); err != nil {
				if os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("scripts[%d] %q: file does not exist inside skill directory", i, p))
				} else {
					errs = append(errs, fmt.Errorf("scripts[%d] %q: cannot stat script: %v", i, p, err))
				}
			} else {
				// Ensure the resolved path is still inside DirPath (no escaping via symlink? simple check).
				// We already rejected "..", but also check clean path does not escape.
				rel, err := filepath.Rel(s.DirPath, fullPath)
				if err == nil && (rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
					errs = append(errs, fmt.Errorf("scripts[%d] %q: must not escape skill directory", i, p))
				}
			}
		}
	}

	// Activation keywords: each non-empty if present.
	for i, kw := range s.ActivationKeywords {
		if strings.TrimSpace(kw) == "" {
			errs = append(errs, fmt.Errorf("activation_keywords[%d]: must not be empty", i))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return wrapInvalid(errors.Join(errs...))
}

func wrapInvalid(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidSkill, err)
}

func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 2 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' {
		return true
	}
	if strings.HasPrefix(p, "\\") {
		return true
	}
	return false
}
