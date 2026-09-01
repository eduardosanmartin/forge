package plugin

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// validateManifest validates m according to the spec rules.
// It aggregates all violations with errors.Join.
func validateManifest(m *Manifest) error {
	var errs []error

	// Name: ^[a-z][a-z0-9_]{1,63}$ (length 2..64, first char lowercase letter)
	nameRe := regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	if m.Name == "" {
		errs = append(errs, errors.New("name: must not be empty"))
	} else if !nameRe.MatchString(m.Name) {
		errs = append(errs, fmt.Errorf("name %q: must match ^[a-z][a-z0-9_]{1,63}$ (lowercase, digits, underscore, no dots, 2-64 chars)", m.Name))
	}

	// Version: strict semver ^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$ with no leading zeros.
	versionRe := regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	if m.Version == "" {
		errs = append(errs, errors.New("version: must not be empty"))
	} else if !versionRe.MatchString(m.Version) {
		errs = append(errs, fmt.Errorf("version %q: must match ^\\d+\\.\\d+\\.\\d+(-[0-9A-Za-z.-]+)?$", m.Version))
	} else {
		core := m.Version
		if idx := strings.Index(core, "-"); idx >= 0 {
			core = core[:idx]
			// prerelease non-empty already ensured by regex; but also ensure it doesn't start/end with '.' or '-'
			// Regex allows those; spec permits dot and hyphen inside; we enforce no leading zeros already for core.
		}
		parts := strings.Split(core, ".")
		if len(parts) != 3 {
			errs = append(errs, fmt.Errorf("version %q: must have three numeric components", m.Version))
		} else {
			for _, p := range parts {
				if len(p) > 1 && p[0] == '0' {
					errs = append(errs, fmt.Errorf("version %q: numeric components must not have leading zeros", m.Version))
					break
				}
			}
		}
		// Pre-release leading zeros not restricted; spec only says no leading zeros in components.
	}

	// Description: non-empty
	if strings.TrimSpace(m.Description) == "" {
		errs = append(errs, errors.New("description: must not be empty"))
	}

	// Source: exactly local or external
	if m.Source != SourceLocal && m.Source != SourceExternal {
		errs = append(errs, fmt.Errorf("source %q: must be %q or %q", m.Source, SourceLocal, SourceExternal))
	}

	// Entrypoint: non-empty, ends with .wasm, no absolute, no ".."
	if m.Entrypoint == "" {
		errs = append(errs, errors.New("entrypoint: must not be empty"))
	} else {
		if !strings.HasSuffix(m.Entrypoint, ".wasm") {
			errs = append(errs, fmt.Errorf("entrypoint %q: must end with \".wasm\"", m.Entrypoint))
		}
		// Absolute check: filepath.IsAbs handles OS-specific; also check "/" prefix and drive letter.
		if isAbsolutePath(m.Entrypoint) {
			errs = append(errs, fmt.Errorf("entrypoint %q: absolute paths are not allowed", m.Entrypoint))
		}
		// Check for ".." path component (normalized with forward slashes).
		normalized := strings.ReplaceAll(m.Entrypoint, "\\", "/")
		segments := strings.Split(normalized, "/")
		for _, seg := range segments {
			if seg == ".." {
				errs = append(errs, fmt.Errorf("entrypoint %q: must not contain \"..\"", m.Entrypoint))
				break
			}
		}
		// Also reject empty segments indicating "//" ?
		// Not needed; but ensure not empty after trimming.
	}

	// Permissions: each in vocab, duplicates rejected, at least one if tools declare.
	allowedPerms := make(map[string]bool, len(PluginPermissionKinds))
	for _, p := range PluginPermissionKinds {
		allowedPerms[p] = true
	}
	seenPerms := make(map[string]bool)
	for i, p := range m.Permissions {
		if !allowedPerms[p] {
			errs = append(errs, fmt.Errorf("permissions[%d] %q: must be one of %v", i, p, PluginPermissionKinds))
		}
		if seenPerms[p] {
			errs = append(errs, fmt.Errorf("permissions[%d] %q: duplicate permission", i, p))
		}
		seenPerms[p] = true
	}
	// If any tool declares a permission, manifest must have at least one permission.
	if len(m.Tools) > 0 && len(m.Permissions) == 0 {
		errs = append(errs, errors.New("permissions: must declare at least one permission when tools are present"))
	}

	// Tools validation
	toolNameRe := regexp.MustCompile(`^[a-z0-9_]+$`)
	seenToolNames := make(map[string]int)
	for i, t := range m.Tools {
		prefix := fmt.Sprintf("%s tool %d", m.Name, i)
		if t.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name must not be empty", prefix))
		} else {
			// Must be prefixed exactly with plugin name + "_"
			expectedPrefix := m.Name + "_"
			if m.Name != "" && !strings.HasPrefix(t.Name, expectedPrefix) {
				errs = append(errs, fmt.Errorf("tools[%d] name %q: must be prefixed with %q", i, t.Name, expectedPrefix))
			} else if m.Name != "" && len(t.Name) == len(expectedPrefix) {
				errs = append(errs, fmt.Errorf("tools[%d] name %q: must have suffix after prefix %q", i, t.Name, expectedPrefix))
			}
			if !toolNameRe.MatchString(t.Name) {
				errs = append(errs, fmt.Errorf("tools[%d] name %q: must match ^[a-z0-9_]+$ (lowercase, digits, underscore, no dots)", i, t.Name))
			}
			if prevIdx, dup := seenToolNames[t.Name]; dup {
				errs = append(errs, fmt.Errorf("tools[%d] name %q: duplicate (also at tools[%d])", i, t.Name, prevIdx))
			} else {
				seenToolNames[t.Name] = i
			}
		}
		if strings.TrimSpace(t.Description) == "" {
			errs = append(errs, fmt.Errorf("tools[%d] %q: description must not be empty", i, t.Name))
		}
		if t.Permission == "" {
			errs = append(errs, fmt.Errorf("tools[%d] %q: permission must not be empty", i, t.Name))
		} else if !seenPerms[t.Permission] {
			// permission must be declared in manifest Permissions
			errs = append(errs, fmt.Errorf("tools[%d] %q: permission %q must be declared in manifest permissions", i, t.Name, t.Permission))
		}
	}

	// Dependencies: each matches nameRe, no self-dependency
	seenDeps := make(map[string]bool)
	for i, dep := range m.Dependencies {
		if !nameRe.MatchString(dep) {
			errs = append(errs, fmt.Errorf("dependencies[%d] %q: must match ^[a-z][a-z0-9_]{1,63}$", i, dep))
		}
		if dep == m.Name {
			errs = append(errs, fmt.Errorf("dependencies[%d] %q: must not be self-dependency", i, dep))
		}
		if seenDeps[dep] {
			errs = append(errs, fmt.Errorf("dependencies[%d] %q: duplicate dependency", i, dep))
		}
		seenDeps[dep] = true
	}

	// Checksum rules
	checksumRe := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	if m.Source == SourceExternal {
		if m.Checksum == "" {
			errs = append(errs, errors.New("checksum: required when source is \"external\""))
		} else if !checksumRe.MatchString(m.Checksum) {
			errs = append(errs, fmt.Errorf("checksum %q: must match ^sha256:[0-9a-f]{64}$", m.Checksum))
		}
	} else if m.Source == SourceLocal {
		if m.Checksum != "" {
			errs = append(errs, errors.New("checksum: must not be set when source is \"local\" (only for external)"))
		}
	} else {
		// Source invalid already reported; if checksum present and format wrong, still report?
		if m.Checksum != "" && !checksumRe.MatchString(m.Checksum) {
			errs = append(errs, fmt.Errorf("checksum %q: must match ^sha256:[0-9a-f]{64}$", m.Checksum))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return wrapInvalid(errors.Join(errs...))
}

// isAbsolutePath reports whether p is an absolute path (POSIX or Windows).
func isAbsolutePath(p string) bool {
	// POSIX absolute
	if strings.HasPrefix(p, "/") {
		return true
	}
	// Windows drive letter e.g., C:\ or C:/ or C:
	if len(p) >= 2 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' {
		return true
	}
	// UNC or backslash absolute
	if strings.HasPrefix(p, "\\") {
		return true
	}
	// filepath.IsAbs handles OS-specific; also check.
	// We avoid importing filepath here to keep this pure, but the cases above cover typical.
	return false
}
