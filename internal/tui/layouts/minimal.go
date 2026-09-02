package layouts

import "strings"

// Minimal renders the minimal layout: transcript + input + footer, no sidebar.
func Minimal(transcript, input, footer string) string {
	return strings.Join([]string{transcript, input, footer}, "\n")
}
