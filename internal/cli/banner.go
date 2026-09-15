package cli

import "fmt"

// forgeLogo is a plain-ASCII wordmark (no Unicode box-drawing) so it renders
// correctly in any terminal, including a plain Windows cmd.exe console.
const forgeLogo = `
#####   ###   ####    ####  #####
#      #   #  #   #  #      #
###    #   #  ####   # ###  ###
#      #   #  #  #   #   #  #
#       ###   #   #   ####  #####
`

// forgeCredits is shown alongside the logo at daemon start and stop.
const forgeCredits = `Forge — local-first agentic development harness
Author: Eduardo San Martín V.
Built mostly with OpenCode, extended (and to a lesser extent built) with Claude Code.`

// printBanner prints the ASCII logo and authorship credits. It writes plain
// text to stdout (not through the structured slog logger) since it's a
// human greeting/farewell, not a log event.
func printBanner() {
	fmt.Print(forgeLogo) // forgeLogo already carries its own leading/trailing newline
	fmt.Println(forgeCredits)
	fmt.Println()
}
