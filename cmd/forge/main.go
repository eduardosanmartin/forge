// Command forge is the entrypoint of the local-first agentic development
// harness. It intentionally contains no logic beyond the isolation-wrapper
// dispatch (which must precede everything, spec RNF-4.7), invoking the CLI
// package, and translating failures into a non-zero exit status: 1 for
// runtime failures, 2 for command-usage mistakes.
package main

import (
	"errors"
	"os"

	"github.com/eduardosanmartin/forge/internal/cli"
	"github.com/eduardosanmartin/forge/internal/isolation"
)

func main() {
	if maybeRunIsolationChild(os.Args[1:], os.Getenv, isolation.RunSelfIsolated, os.Exit) {
		return
	}

	if err := cli.Execute(); err != nil {
		var usageErr *cli.UsageError
		if errors.As(err, &usageErr) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
