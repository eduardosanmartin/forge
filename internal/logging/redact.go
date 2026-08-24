package logging

import (
	"regexp"
	"sync"
)

// redactedPlaceholder replaces every matched secret.
const redactedPlaceholder = "[REDACTED]"

// redactPattern pairs a compiled expression with the replacement applied to
// each match. Patterns whose replacement must preserve part of the match
// (for example, a key name) embed capture references themselves.
type redactPattern struct {
	re          *regexp.Regexp
	replacement string
}

// baseRedactPatterns is forge's best-effort secret-redaction baseline,
// compiled once at init and never mutated afterwards. It is a heuristic
// baseline, not a guarantee: novel secret formats can still leak.
var baseRedactPatterns = []redactPattern{
	// AWS access key IDs (AKIA followed by 16 uppercase letters/digits).
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), redactedPlaceholder},
	// Private key PEM header lines (BEGIN RSA/EC/OPENSSH/... PRIVATE KEY).
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), redactedPlaceholder},
	// OpenAI-style API keys.
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), redactedPlaceholder},
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_ prefixes).
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), redactedPlaceholder},
	// Generic key=value / key: value assignments for common secret names;
	// the key name is preserved, only the value is redacted. Case-insensitive.
	{
		re:          regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret|token|password|passwd|authorization|bearer)\b(\s*[:=]\s*)("[^"\n]{8,}"|[^\s"',;]{8,})`),
		replacement: `${1}${2}` + redactedPlaceholder,
	},
}

var (
	redactMu sync.RWMutex
	// extraRedactPatterns holds patterns registered at runtime via
	// AddRedactPatterns. The base set above stays immutable after init.
	extraRedactPatterns []redactPattern
)

// AddRedactPatterns registers additional patterns applied by Redact in
// addition to the immutable baseline. Each added pattern replaces its matches
// with "[REDACTED]"; callers that need partial preservation should embed
// capture-group references in a custom pattern via this entry point's
// sibling APIs or pre-process text before logging. Nil entries are ignored.
//
// Safe for concurrent use.
func AddRedactPatterns(patterns ...*regexp.Regexp) {
	redactMu.Lock()
	defer redactMu.Unlock()
	for _, re := range patterns {
		if re != nil {
			extraRedactPatterns = append(extraRedactPatterns, redactPattern{re: re, replacement: redactedPlaceholder})
		}
	}
}

// activePatterns snapshots the full pattern list (baseline plus extras).
func activePatterns() []redactPattern {
	redactMu.RLock()
	defer redactMu.RUnlock()
	if len(extraRedactPatterns) == 0 {
		return baseRedactPatterns
	}
	all := make([]redactPattern, 0, len(baseRedactPatterns)+len(extraRedactPatterns))
	all = append(all, baseRedactPatterns...)
	all = append(all, extraRedactPatterns...)
	return all
}

// Redact returns s with every recognized secret replaced by "[REDACTED]".
// Specific credential formats run first; the generic assignment pattern runs
// last so that structured "name: value" pairs keep their key names. Redact is
// idempotent on its own output.
func Redact(s string) string {
	for _, p := range activePatterns() {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, p.replacement)
		}
	}
	return s
}
