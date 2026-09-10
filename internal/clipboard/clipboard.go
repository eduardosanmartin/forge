// Package clipboard writes text to the OS clipboard without cgo, using the
// platform-native tool over stdin: powershell Set-Clipboard (Windows),
// pbcopy (macOS), xclip/xsel/wl-copy first-available (Linux).
package clipboard

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// candidateCommands lists clipboard commands to try in order for a GOOS.
// Pure function (no side effects) so selection stays unit-testable.
func candidateCommands(goos string) [][]string {
	switch goos {
	case "windows":
		return [][]string{{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Set-Clipboard"}}
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "linux":
		return [][]string{
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
			{"wl-copy"},
		}
	default:
		return nil
	}
}

// Write copies text to the OS clipboard. Empty text is a no-op returning
// nil (clearing the clipboard by accident is never what a caller wants;
// callers surface "nothing to copy" themselves).
func Write(text string) error {
	if text == "" {
		return nil
	}
	cands := candidateCommands(runtime.GOOS)
	if len(cands) == 0 {
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	var errs []string
	for _, c := range cands {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
		errs = append(errs, c[0]+": "+err.Error()+strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("clipboard failed: %s", strings.Join(errs, "; "))
}
