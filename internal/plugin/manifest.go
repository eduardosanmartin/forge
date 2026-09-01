// Package plugin implements the forge plugin ABI and manifest schema.
//
// Manifest TOML shape this package accepts (documented contract for WU4 wizard):
//
//	name = "my_plugin"
//	version = "0.1.0"
//	description = "Example plugin."
//	source = "local"
//	entrypoint = "plugin.wasm"
//	permissions = ["fs.read", "git"]
//	dependencies = []
//	checksum = "sha256:..."   # only for external source
//
//	[[tools]]
//	name = "my_plugin_greet"
//	description = "Greets a user."
//	permission = "fs.read"
package plugin

import (
	"errors"
	"fmt"
)

// Source identifies the origin of a plugin.
type Source string

const (
	// SourceLocal indicates a plugin created locally (no checksum required).
	SourceLocal Source = "local"
	// SourceExternal indicates a plugin from an external source (checksum required).
	SourceExternal Source = "external"
)

// ToolExport describes a tool exported by a plugin.
type ToolExport struct {
	// Name is the exported tool name; MUST be "<plugin-name>_<tool>" (OpenAI-safe: lowercase, digits, underscore, no dots).
	Name string
	// Description is a human-readable description (non-empty).
	Description string
	// Permission is the permission required to invoke this tool; must be declared in the manifest's Permissions list.
	Permission string
}

// Manifest describes a plugin's metadata, permissions, and exported tools.
type Manifest struct {
	// Name is the plugin name.
	Name string
	// Version is the strict semver MAJOR.MINOR.PATCH with optional -prerelease.
	Version string
	// Description is a non-empty description of the plugin.
	Description string
	// Source indicates whether the plugin is local or external.
	Source Source
	// Entrypoint is the path to the .wasm file, must end in ".wasm".
	Entrypoint string
	// Permissions is the list of permissions requested by the plugin.
	Permissions []string
	// Dependencies is an optional list of plugin names this plugin depends on.
	Dependencies []string
	// Checksum is the expected sha256 checksum for external plugins (format "sha256:<64 lowercase hex>").
	Checksum string
	// Tools is the list of tools exported by the plugin.
	Tools []ToolExport
}

// ParseManifest parses and validates a plugin manifest from TOML data.
// It is the only entry point callers need; parsing and validation run as a single step.
// On failure it returns a joined error aggregating all violations where possible.
func ParseManifest(data []byte) (Manifest, error) {
	m, err := parseTOML(data)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(&m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ErrInvalidManifest is a sentinel for manifest validation failures.
var ErrInvalidManifest = errors.New("invalid manifest")

// wrapInvalid wraps err with ErrInvalidManifest for sentinel + %w chaining.
func wrapInvalid(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidManifest, err)
}
