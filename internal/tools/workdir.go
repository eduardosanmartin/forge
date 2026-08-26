package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// validateWorkdir resolves a caller-supplied workdir for shell/git tools.
//
// Local models frequently hallucinate plausible-looking directories (the
// classic offender is "/workspace") because tool schemas expose the field.
// Rather than letting `git -C` or cmd.Dir fail with an opaque OS error, this
// returns an actionable ERROR result prefix so the model can self-correct on
// its next iteration by simply omitting the argument.
//
// An empty workdir means "current process working directory" (the workspace
// root by daemon launch convention) and is valid by definition.
func validateWorkdir(workdir string) (string, error) {
	if workdir == "" {
		return "", nil // inherit process CWD
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("invalid workdir %q: %v", workdir, err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		cwd, _ := os.Getwd()
		return "", fmt.Errorf(
			"workdir %q does not exist in this environment (it looks invented). "+
				"RETRY the same call OMITTING the workdir argument entirely to run in the current workspace root: %s",
			workdir, cwd)
	}
	return abs, nil
}
