// Package version carries forge build metadata.
//
// The variables below are plain package vars so release tooling can set them
// at link time with -ldflags -X, for example:
//
//	go build -ldflags "-X github.com/eduardosanmartin/forge/internal/version.Version=v1.2.3 \
//	                   -X github.com/eduardosanmartin/forge/internal/version.Commit=abc123 \
//	                   -X github.com/eduardosanmartin/forge/internal/version.Date=2026-08-24T00:00:00Z"
package version

import "fmt"

var (
	// Version is the semantic version of this build; "0.0.0-dev" when unset.
	Version = "0.0.0-dev"

	// Commit identifies the source revision the binary was built from.
	Commit = "none"

	// Date is the RFC3339 build timestamp when provided by the builder.
	Date = "unknown"
)

// String renders the canonical one-line version banner reported by
// `forge version`.
func String() string {
	return fmt.Sprintf("forge version %s (commit %s, built %s)", Version, Commit, Date)
}
