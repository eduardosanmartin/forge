// Package config implements forge's versioned, mergeable configuration.
//
// Configuration is layered field-group-wise: built-in defaults are overridden
// by the global file, which is overridden by the project file (or an explicit
// --config path). Later files replace earlier values per section; named
// provider entries are replaced wholesale. Missing files are skipped
// silently, but present-yet-invalid files are hard errors.
//
// All path fields ("storage.path", "logging.file") have a leading "~/"
// expanded at load time via os.UserHomeDir.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/pathmatch"
)

// CurrentSchemaVersion is the newest configuration schema revision this
// build understands. Version 2 added the "permissions" section (RNF-4.1).
// Version 3 added permissions.shell.require_isolation (RNF-4.7), which
// makes Linux refuse shell_exec when OS-level isolation is unavailable
// instead of silently degrading; non-Linux platforms ignore it.
// Version 4 added the optional providers.<name>.model_roles map for
// cost-based model routing (RF-2.4/2.5); a v3 document needs no data
// change to be valid v4.
const CurrentSchemaVersion = 4

// Provider describes a single OpenAI-compatible inference endpoint.
type Provider struct {
	Kind       string            `json:"kind"`
	BaseURL    string            `json:"base_url"`
	Models     []string          `json:"models"`
	ModelRoles map[string]string `json:"model_roles,omitempty"`
	// APIKey authenticates against remote endpoints. Empty falls back to the
	// OPENCODE_API_KEY env var so secrets stay out of config files.
	APIKey string `json:"api_key"`
}

// StorageConfig locates forge's local database.
type StorageConfig struct {
	Path string `json:"path"`
}

// NetworkConfig bounds outbound network access.
type NetworkConfig struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

// LoggingConfig selects log verbosity and an optional extra destination.
type LoggingConfig struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

// FSPermissions bounds filesystem access with glob patterns. Relative
// patterns match workspace-relative paths; absolute patterns (POSIX-rooted
// or drive-letter form, forward-slashed) are the documented escape hatch for
// explicitly authorized out-of-workspace locations.
type FSPermissions struct {
	Read  []string `json:"read"`
	Write []string `json:"write"`
}

// ShellPermissions allows shell executables by base name (case-insensitive).
// RequireIsolation (schema v3, RNF-4.7) asks forge to refuse shell
// execution on Linux when OS-level isolation (Landlock + seccomp wrapper)
// is unavailable, rather than degrading to permissions-only enforcement.
// Non-Linux platforms ignore the flag: macOS v0 and Windows are
// documented as permissions-only per spec §6.
type ShellPermissions struct {
	Allow            []string `json:"allow"`
	RequireIsolation bool     `json:"require_isolation"`
}

// GitPermissions allows git subcommands (lowercase convention). Destructive
// invocations stay blocked by the engine's non-configurable safety floor
// regardless of this list (RNF-8.2).
type GitPermissions struct {
	Allow []string `json:"allow"`
}

// PermissionsPolicy mirrors the "permissions" section of the config
// document. It is deny-by-default: anything not explicitly allowed is
// refused by the permission engine (RNF-4.1).
type PermissionsPolicy struct {
	FS    FSPermissions    `json:"fs"`
	Shell ShellPermissions `json:"shell"`
	Git   GitPermissions   `json:"git"`
}

// defaultPermissionsPolicy returns the built-in baseline policy:
// workspace-wide filesystem access under the default deny posture for
// everything else, an EMPTY shell allowlist (RNF-4.1: nothing runs unless
// declared) with OS isolation required on capable platforms (RNF-4.7), and
// a conventional read-only-plus-staging git allowlist.
func defaultPermissionsPolicy() PermissionsPolicy {
	return PermissionsPolicy{
		FS:    FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: ShellPermissions{Allow: []string{}, RequireIsolation: true},
		Git: GitPermissions{Allow: []string{
			"status", "add", "commit", "log", "diff", "branch",
			"switch", "stash", "restore", "show", "remote", "fetch",
		}},
	}
}

// Config is the full forge configuration document.
type Config struct {
	SchemaVersion   int                 `json:"schema_version"`
	DefaultProvider string              `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         StorageConfig       `json:"storage"`
	Network         NetworkConfig       `json:"network"`
	Logging         LoggingConfig       `json:"logging"`
	Permissions     PermissionsPolicy   `json:"permissions"`
}

// Defaults returns the built-in baseline configuration. Callers may treat the
// returned value as a fresh, unshared instance.
func Defaults() *Config {
	return &Config{
		SchemaVersion:   CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
				Models:  []string{"qwen2.5-coder:7b"},
				ModelRoles: map[string]string{
					"cheap":      "qwen2.5-coder:1.5b",
					"generation": "qwen2.5-coder:7b",
					"reasoning":  "relational/VULCAN",
				},
			},
		},
		Storage:     StorageConfig{Path: "~/.forge/forge.db"},
		Network:     NetworkConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}},
		Logging:     LoggingConfig{Level: "info", File: ""},
		Permissions: defaultPermissionsPolicy(),
	}
}

// ExpandPath expands a leading "~/" (or bare "~") in p to the current user's
// home directory and normalizes separators. Paths without the prefix are
// returned unchanged. Expansion failure (for example, when no home directory
// is defined) returns an error rather than a silently mangled path.
func ExpandPath(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand path %q: resolve home directory: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "~/"), `~\`)
	return filepath.Join(home, filepath.FromSlash(rest)), nil
}

// GlobalConfigPath returns the user-wide configuration path
// (~/.forge/config.json), with "~" expanded.
func GlobalConfigPath() (string, error) {
	p, err := ExpandPath("~/.forge/config.json")
	if err != nil {
		return "", fmt.Errorf("resolve global config path: %w", err)
	}
	return p, nil
}

// ProjectConfigPath returns the project-scoped configuration path
// (./.forge/config.json).
func ProjectConfigPath() (string, error) {
	return ExpandPath(filepath.Join(".", ".forge", "config.json"))
}

// filePermissions mirrors PermissionsPolicy with presence-tracking pointers
// so merging can replace each subsection (fs/shell/git) wholesale only when
// that subsection is present in an overriding document.
type filePermissions struct {
	FS    *FSPermissions    `json:"fs"`
	Shell *ShellPermissions `json:"shell"`
	Git   *GitPermissions   `json:"git"`
}

// fileConfig mirrors Config with presence-tracking pointers so that merging
// can distinguish "field absent" from "field set to zero value".
type fileConfig struct {
	SchemaVersion   *int                `json:"schema_version"`
	DefaultProvider *string             `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         *StorageConfig      `json:"storage"`
	Network         *NetworkConfig      `json:"network"`
	Logging         *LoggingConfig      `json:"logging"`
	Permissions     *filePermissions    `json:"permissions"`
}

// Load builds a Config from defaults overlaid with the given files in order:
// later files override earlier values field-group-wise (provider entries are
// replaced wholesale per named provider; scalar sections are replaced whole
// whenever present; permissions subsections fs/shell/git each replace whole
// when present). Missing files are skipped silently; present but invalid
// files produce an error that names the offending path. Documents older than
// the current schema version are migrated forward before decoding; the
// returned Config always describes current-schema semantics.
func Load(filePaths ...string) (*Config, error) {
	cfg := Defaults()

	for _, path := range filePaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read config file %s: %w", path, err)
		}

		// Probe the schema version first (with unknown-field rejection, so a
		// typo still fails fast even before migration).
		var probe fileConfig
		probeDec := json.NewDecoder(bytes.NewReader(raw))
		probeDec.DisallowUnknownFields()
		if err := probeDec.Decode(&probe); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		version := CurrentSchemaVersion
		if probe.SchemaVersion != nil {
			version = *probe.SchemaVersion
		}
		if version < 1 || version > CurrentSchemaVersion {
			return nil, fmt.Errorf(
				"config file %s: unsupported schema_version %d (supported range: %d..%d)",
				path, version, 1, CurrentSchemaVersion,
			)
		}

		doc := raw
		if version != CurrentSchemaVersion {
			migrated, err := Migrate(raw, version)
			if err != nil {
				return nil, fmt.Errorf("config file %s: %w", path, err)
			}
			doc = migrated
		}

		var fc fileConfig
		dec := json.NewDecoder(bytes.NewReader(doc))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fc); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		mergeInto(cfg, &fc)

		// A migrated document now describes current-schema semantics in
		// memory (the file on disk is untouched until Save).
		if version != CurrentSchemaVersion {
			cfg.SchemaVersion = CurrentSchemaVersion
		}
	}

	if err := cfg.expandPaths(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeInto applies every field group present in fc onto dst.
func mergeInto(dst *Config, fc *fileConfig) {
	if fc.SchemaVersion != nil {
		dst.SchemaVersion = *fc.SchemaVersion
	}
	if fc.DefaultProvider != nil {
		dst.DefaultProvider = *fc.DefaultProvider
	}
	for name, p := range fc.Providers {
		dst.Providers[name] = p
	}
	if fc.Storage != nil {
		dst.Storage = *fc.Storage
	}
	if fc.Network != nil {
		dst.Network = *fc.Network
	}
	if fc.Logging != nil {
		dst.Logging = *fc.Logging
	}
	if fc.Permissions != nil {
		// Group-wise merge: each present subsection (fs/shell/git) replaces
		// the corresponding policy group wholesale, mirroring how scalar
		// sections behave. Lists inside a present subsection are taken as-is.
		fp := fc.Permissions
		if fp.FS != nil {
			dst.Permissions.FS = *fp.FS
		}
		if fp.Shell != nil {
			dst.Permissions.Shell = *fp.Shell
		}
		if fp.Git != nil {
			dst.Permissions.Git = *fp.Git
		}
	}
}

// expandPaths expands "~" in every path field of c in place.
func (c *Config) expandPaths() error {
	expanded, err := ExpandPath(c.Storage.Path)
	if err != nil {
		return fmt.Errorf("config storage.path: %w", err)
	}
	c.Storage.Path = expanded

	expanded, err = ExpandPath(c.Logging.File)
	if err != nil {
		return fmt.Errorf("config logging.file: %w", err)
	}
	c.Logging.File = expanded
	return nil
}

// Migrate forwards raw config JSON from schema version from to the current
// schema version, applying the migration chain one step at a time. The
// current version returns data unchanged; versions without a migration step
// report an error naming both versions.
func Migrate(data []byte, from int) ([]byte, error) {
	switch from {
	case CurrentSchemaVersion:
		return data, nil
	case 1:
		// Chain: v1 gains the permissions section (v2), then the shell
		// isolation flag (v3), then the optional provider model_roles
		// map (v4, no data change).
		v2, err := migrateV1ToV2(data)
		if err != nil {
			return nil, err
		}
		v3, err := migrateV2ToV3(v2)
		if err != nil {
			return nil, err
		}
		return migrateV3ToV4(v3)
	case 2:
		v3, err := migrateV2ToV3(data)
		if err != nil {
			return nil, err
		}
		return migrateV3ToV4(v3)
	case 3:
		return migrateV3ToV4(data)
	default:
		return nil, fmt.Errorf(
			"no migration path from schema_version %d to schema_version %d",
			from, CurrentSchemaVersion,
		)
	}
}

// migrateV1ToV2 upgrades a v1 document to v2 by injecting the built-in
// permissions policy when the document predates the section (schema v2 added
// "permissions", RNF-4.1). All other fields are preserved verbatim; a
// document that already carries a permissions key is left untouched so
// hand-written forward-looking sections are never clobbered.
func migrateV1ToV2(data []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: parse document: %w", err)
	}
	if _, exists := doc["permissions"]; exists {
		return data, nil
	}
	def, err := json.Marshal(defaultPermissionsPolicy())
	if err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: encode default permissions: %w", err)
	}
	doc["permissions"] = json.RawMessage(def)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: encode migrated document: %w", err)
	}
	return out, nil
}

// migrateV2ToV3 upgrades a v2 document to v3 by ensuring
// permissions.shell carries require_isolation=true (schema v3 added the
// flag, RNF-4.7). Absence in a v2 document meant "before the field
// existed", so it upgrades to the secure default rather than Go's zero
// value; an explicitly written false is preserved verbatim. Sibling fields
// (shell.allow, fs, git) and documents without a permissions section at all
// are left untouched — merging falls back to Defaults(), which already
// requires isolation.
func migrateV2ToV3(data []byte) ([]byte, error) {
	const step = "migrate schema v2 -> v3"
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: parse document: %w", step, err)
	}
	permsRaw, exists := doc["permissions"]
	if !exists {
		// No permissions section: merge supplies Defaults(), whose shell
		// policy requires isolation.
		return data, nil
	}

	var perms map[string]json.RawMessage
	if err := json.Unmarshal(permsRaw, &perms); err != nil {
		return nil, fmt.Errorf("%s: parse permissions section: %w", step, err)
	}

	var shell map[string]json.RawMessage
	if shellRaw, exists := perms["shell"]; exists {
		if err := json.Unmarshal(shellRaw, &shell); err != nil {
			return nil, fmt.Errorf("%s: parse permissions.shell section: %w", step, err)
		}
	} else {
		shell = make(map[string]json.RawMessage)
	}

	if _, exists := shell["require_isolation"]; !exists {
		shell["require_isolation"] = json.RawMessage("true")
	}

	shellOut, err := json.Marshal(shell)
	if err != nil {
		return nil, fmt.Errorf("%s: encode permissions.shell: %w", step, err)
	}
	perms["shell"] = json.RawMessage(shellOut)
	permsOut, err := json.Marshal(perms)
	if err != nil {
		return nil, fmt.Errorf("%s: encode permissions: %w", step, err)
	}
	doc["permissions"] = json.RawMessage(permsOut)

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: encode migrated document: %w", step, err)
	}
	return out, nil
}

// migrateV3ToV4 upgrades a v3 document to v4. Schema v4 added the OPTIONAL
// providers.<name>.model_roles map (Provider.ModelRoles, cost-based model
// routing, RF-2.4/2.5): absence in a v3 document already means "no roles
// assigned" (the Go zero value), so no data transformation is needed and
// the document passes through unchanged. The step exists so the migration
// chain can express v1 -> v4 and Load accepts v3 files instead of rejecting
// them with "no migration path" (the gap the v4 bump originally left).
func migrateV3ToV4(data []byte) ([]byte, error) {
	return data, nil
}

// Validate checks c against all configuration rules and returns an aggregated
// error listing every violation found (nil when the config is valid). An
// empty logging.level is normalized to "info" before validation. Path fields
// are expected to be pre-expanded (Load does this); validation only requires
// them to be non-empty.
func (c *Config) Validate() error {
	var violations []error

	if strings.TrimSpace(c.DefaultProvider) == "" {
		violations = append(violations, errors.New("default_provider must not be empty"))
	} else if _, ok := c.Providers[c.DefaultProvider]; !ok {
		violations = append(violations, fmt.Errorf(
			"default_provider %q does not match any entry in providers", c.DefaultProvider))
	}

	for name, p := range c.Providers {
		label := fmt.Sprintf("provider %q", name)
		if p.Kind != "openai-compatible" {
			violations = append(violations, fmt.Errorf(
				"%s: kind must be \"openai-compatible\", got %q", label, p.Kind))
		}
		u, err := url.Parse(p.BaseURL)
		switch {
		case err != nil:
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q is not a valid URL: %v", label, p.BaseURL, err))
		case u.Scheme != "http" && u.Scheme != "https":
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q must be an absolute http(s) URL", label, p.BaseURL))
		case u.Host == "":
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q is missing a host", label, p.BaseURL))
		}
		if len(p.Models) < 1 {
			violations = append(violations, fmt.Errorf(
				"%s: must declare at least one model", label))
		}
	}

	if strings.TrimSpace(c.Storage.Path) == "" {
		violations = append(violations, errors.New("storage.path must not be empty"))
	}

	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		violations = append(violations, fmt.Errorf(
			"logging.level %q is invalid (allowed: debug, info, warn, error)", c.Logging.Level))
	}

	for i, host := range c.Network.AllowedHosts {
		if strings.TrimSpace(host) == "" {
			violations = append(violations, fmt.Errorf(
				"network.allowed_hosts[%d]: entries must be non-empty host names", i))
		}
	}

	violations = append(violations, validatePermissions(c.Permissions)...)

	return errors.Join(violations...)
}

// validatePermissions checks the permissions section structurally. Glob
// syntax authority lives in internal/pathmatch (imported here so config and
// perms can never drift apart); this function only adds section context to
// every violation it reports.
func validatePermissions(p PermissionsPolicy) []error {
	var errs []error
	for i, pat := range p.FS.Read {
		if err := pathmatch.ValidatePattern(pat); err != nil {
			errs = append(errs, fmt.Errorf("permissions.fs.read[%d] %q: %v", i, pat, err))
		}
	}
	for i, pat := range p.FS.Write {
		if err := pathmatch.ValidatePattern(pat); err != nil {
			errs = append(errs, fmt.Errorf("permissions.fs.write[%d] %q: %v", i, pat, err))
		}
	}
	for i, entry := range p.Shell.Allow {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.shell.allow[%d]: entries must be non-empty command names", i))
		}
	}
	for i, entry := range p.Git.Allow {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.git.allow[%d]: entries must be non-empty subcommands", i))
		}
	}
	return errs
}

// Save writes c to path atomically: the marshaled document (two-space indent
// plus trailing newline) lands in a temp file inside the same directory,
// which is then renamed over path. Missing parent directories are created.
func (c *Config) Save(path string) error {
	path, err := ExpandPath(path)
	if err != nil {
		return fmt.Errorf("resolve config save path: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("set permissions on temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace config file %s: %w", path, err)
	}
	return nil
}
