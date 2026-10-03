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
	// Whole private key PEM blocks (BEGIN ... PRIVATE KEY through the
	// matching END line). Redacting only the header line leaked the base64
	// body, which IS the key. A block whose END line is missing (output
	// truncated mid-key) is redacted through the end of the text.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), redactedPlaceholder},
	// OpenAI-style API keys (also Anthropic sk-ant-... and OpenRouter sk-or-...).
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`), redactedPlaceholder},
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_ prefixes).
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), redactedPlaceholder},
	// Google API keys (AIza + 35 chars).
	{regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), redactedPlaceholder},
	// Slack tokens (xoxb-/xoxa-/xoxp-/xoxr-/xoxs-).
	{regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`), redactedPlaceholder},
	// JSON Web Tokens (three base64url segments, header and payload are JSON
	// objects so both start with "eyJ").
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redactedPlaceholder},
	// Passwords in URLs (user:pass@host).
	{regexp.MustCompile(`(://[^:/\s]+):([^@/\s]{8,})@`), `${1}:` + redactedPlaceholder + `@`},
	// Generic key=value / key: value assignments for common secret names;
	// the key name is preserved, only the value is redacted. Case-insensitive.
	// The key may carry a prefix (GEMINI_API_KEY, access_token) and a short
	// suffix (secret_key, api_key_id), and may be quoted ("api_key": "...")
	// — JSON/YAML configs were previously passed through untouched because
	// the closing quote sat between the key and the separator.
	// Quoted values keep their quotes (JSON stays valid); the unquoted
	// pattern below can't re-match a quoted placeholder, so Redact stays
	// idempotent.
	{
		re:          regexp.MustCompile(secretKeyName + `("?\s*[:=]\s*)"[^"\n]{8,}"`),
		replacement: `${1}${2}"` + redactedPlaceholder + `"`,
	},
	{
		re:          regexp.MustCompile(secretKeyName + `("?\s*[:=]\s*)[^\s"',;]{8,}`),
		replacement: `${1}${2}` + redactedPlaceholder,
	},
}

// secretKeyName matches (as capture group 1) a key name that conventionally
// holds a secret, for the generic assignment patterns above.
const secretKeyName = `(?i)\b((?:[a-z0-9]+[_-])*(?:api[_-]?key|apikey|secret|token|password|passwd|authorization|bearer)(?:[_-](?:key|id|value|token|secret))?)\b`

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
