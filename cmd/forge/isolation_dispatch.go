// Isolation-wrapper dispatch: when forge is re-executed by its own tools
// layer as an isolation wrapper child (internal/isolation), it must apply
// Landlock + seccomp and replace its image BEFORE any CLI processing, so
// wrapper invocations never surface as commands or flags. The check runs
// first in main() and is extracted here (with injectable seams) so the
// routing decision is unit-testable without spawning processes.
package main

import (
	"fmt"
	"os"

	"github.com/eduardosanmartin/forge/internal/isolation"
)

// maybeRunIsolationChild inspects env for the wrapper marker. When absent
// it returns false and normal CLI flow continues. When present it hands
// argv to run (isolation.RunSelfIsolated): on success run never returns
// (the image is replaced); on failure fatal reports exit code 126 — the
// conventional "command found but not executable" status used because the
// isolated child could not set up or exec its target.
func maybeRunIsolationChild(
	argv []string,
	getenv func(string) string,
	run func([]string) error,
	fatal func(int),
) bool {
	if getenv(isolation.ChildEnvVar) != "1" {
		return false
	}
	if err := run(argv); err != nil {
		fmt.Fprintf(os.Stderr, "forge isolation: %v\n", err)
		fatal(126)
	}
	return true
}
