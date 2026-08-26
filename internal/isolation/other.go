//go:build !linux

// Platform half of the isolation package for every non-Linux OS (macOS,
// Windows, BSDs). Spec RNF-4.7 §6: v0 restricts OS-level shell enforcement
// to Linux (Landlock + seccomp are stable, documented primitives there).
// macOS sandbox-exec is an undocumented, deprecated Apple API and is not an
// acceptable foundation; Windows has no comparable in-kernel primitive
// exposed to Go. On these platforms the permission model (RNF-4.1) remains
// the sole enforcement layer and this package is a documented no-op.
package isolation

import "errors"

// detectCapabilities reports that this platform cannot enforce OS-level
// shell isolation. Compile-time resolved; never changes at runtime.
func detectCapabilities() Capability {
	return Capability{
		OSIsolation: false,
		Reason:      "os-level shell isolation is only implemented on Linux (RNF-4.7); permissions model remains active",
	}
}

// RunSelfIsolated is the wrapper-child entry point on non-Linux platforms.
// It must never be reached in normal operation — the parent only wraps on
// capable platforms — so any invocation here is a wiring bug and errors out
// loudly instead of silently running unrestricted.
func RunSelfIsolated(args []string) error {
	return errors.New("isolation child invoked on unsupported platform")
}
