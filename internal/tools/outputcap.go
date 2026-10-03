package tools

import (
	"fmt"
	"unicode/utf8"
)

// Output caps for what a single tool result puts into the model's context
// (RNF-2.5: interactive context target of 4-8k tokens). Previously fs_read
// returned whole files and shell_exec up to 50 KB (~12k tokens) per call,
// so one result could blow the whole budget and then stay in the history
// window for the following turns. The full data is still reachable: fs_read
// pages with offset/limit, and a command can be re-run narrowed down.
const (
	// DefaultReadLimitBytes is fs_read's page size when no limit is given.
	DefaultReadLimitBytes = 16 * 1024
	// ShellHeadBytes/ShellTailBytes are how much of an over-long command
	// output is kept from its start and its end. The tail gets more room:
	// compilers, test runners and stack traces print the failure last.
	ShellHeadBytes = 4 * 1024
	ShellTailBytes = 8 * 1024
)

// utf8PrefixLen returns the largest n <= max such that b[:n] doesn't end
// in the middle of a UTF-8 sequence.
func utf8PrefixLen(b []byte, max int) int {
	if max >= len(b) {
		return len(b)
	}
	n := max
	for n > 0 && n > max-utf8.UTFMax && !utf8.RuneStart(b[n]) {
		n--
	}
	return n
}

// utf8SuffixStart returns the smallest index i >= len(b)-max such that b[i:]
// starts on a UTF-8 rune boundary.
func utf8SuffixStart(b []byte, max int) int {
	if max >= len(b) {
		return 0
	}
	i := len(b) - max
	for i < len(b) && i < len(b)-max+utf8.UTFMax && !utf8.RuneStart(b[i]) {
		i++
	}
	return i
}

// headTail keeps the first head and last tail bytes of out (on rune
// boundaries) with a marker naming how much was dropped in between. It
// reports whether anything was dropped.
func headTail(out []byte, head, tail int) ([]byte, bool) {
	if len(out) <= head+tail {
		return out, false
	}
	h := utf8PrefixLen(out, head)
	t := utf8SuffixStart(out, tail)
	marker := fmt.Sprintf("\n\n[... %d bytes omitted — output truncated to its first %d and last %d bytes; re-run with narrower output (e.g. a filter or a single test) to see more ...]\n\n", t-h, h, len(out)-t)
	res := make([]byte, 0, h+len(marker)+len(out)-t)
	res = append(res, out[:h]...)
	res = append(res, marker...)
	res = append(res, out[t:]...)
	return res, true
}
