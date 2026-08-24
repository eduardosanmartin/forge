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
)

// CurrentSchemaVersion is the newest configuration schema revision this
// build understands.
const CurrentSchemaVersion = 1

// Provider describes a single OpenAI-compatible inference endpoint.
type Provider struct {
	Kind    string   `json:"kind"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
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

// Config is the full forge configuration document.
type Config struct {
	SchemaVersion   int                 `json:"schema_version"`
	DefaultProvider string              `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         StorageConfig       `json:"storage"`
	Network         NetworkConfig       `json:"network"`
	Logging         LoggingConfig       `json:"logging"`
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
			},
		},
		Storage: StorageConfig{Path: "~/.forge/forge.db"},
		Network: NetworkConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}},
		Logging: LoggingConfig{Level: "info", File: ""},
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

// fileConfig mirrors Config with presence-tracking pointers so that merging
// can distinguish "field absent" from "field set to zero value".
type fileConfig struct {
	SchemaVersion   *int                `json:"schema_version"`
	DefaultProvider *string             `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         *StorageConfig      `json:"storage"`
	Network         *NetworkConfig      `json:"network"`
	Logging         *LoggingConfig      `json:"logging"`
}

// Load builds a Config from defaults overlaid with the given files in order:
// later files override earlier values field-group-wise (provider entries are
// replaced wholesale per named provider; scalar sections are replaced whole
// whenever present). Missing files are skipped silently; present but invalid
// files produce an error that names the offending path.
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

		var fc fileConfig
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fc); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		version := CurrentSchemaVersion
		if fc.SchemaVersion != nil {
			version = *fc.SchemaVersion
		}
		if version < 1 || version > CurrentSchemaVersion {
			return nil, fmt.Errorf(
				"config file %s: unsupported schema_version %d (supported range: %d..%d)",
				path, version, 1, CurrentSchemaVersion,
			)
		}
		if version != CurrentSchemaVersion {
			if _, err := Migrate(raw, version); err != nil {
				return nil, fmt.Errorf("config file %s: %w", path, err)
			}
		}

		mergeInto(cfg, &fc)
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
// schema version. It is a forward-migration scaffold: only the current
// schema exists today, so any other version reports that no migration path
// is implemented yet.
func Migrate(data []byte, from int) ([]byte, error) {
	if from == CurrentSchemaVersion {
		return data, nil
	}
	return nil, fmt.Errorf(
		"no migration path from schema_version %d to schema_version %d",
		from, CurrentSchemaVersion,
	)
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

	return errors.Join(violations...)
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
