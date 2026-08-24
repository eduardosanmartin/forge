// Command forge is the entrypoint of the local-first agentic development
// harness. It intentionally contains no logic beyond invoking the CLI package
// and translating failures into a non-zero exit status.
package main

import (
	"os"

	"github.com/eduardosanmartin/forge/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
