// Package llmjson holds small, shared parsing helpers for extracting a JSON
// array out of a model's raw text response, tolerating the ways models
// routinely deviate from "respond with ONLY JSON" instructions (markdown
// code fences, trailing prose). Extracted from internal/run/decompose.go
// (RF-11's task decomposition) so internal/bootstrap (the intelligent
// wizard, hojaDeRuta-wizard-inteligente.md) reuses the same hardened
// extraction instead of a second, subtly-different copy.
package llmjson

import "strings"

// StripCodeFence removes a single leading/trailing markdown code fence
// (``` or ```json) if present; returns s unchanged otherwise.
func StripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// OutermostBrackets returns the span [start, end) of the JSON array starting
// at the first '[' in s, found by tracking bracket depth (not just "first
// '[' to last ']'" — a naive last-']' scan grabs trailing prose as part of
// the array whenever the model's own commentary after the array contains
// ANY ']' of its own, e.g. a markdown link or an "arr[0]" example; observed
// in practice against a real decomposition response and exactly the kind of
// response shape small/local models are prone to). Bracket/brace depth
// inside JSON string literals is ignored via a small state machine so a
// string value like "returns arr[0]" doesn't miscount.
func OutermostBrackets(s string) (start, end int, ok bool) {
	start = strings.IndexByte(s, '[')
	if start < 0 {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return 0, 0, false
}

// Candidates returns the progressively looser extraction attempts a caller
// should try in order against a model's raw response: the raw text as-is,
// then with a code fence stripped, then the outermost bracketed JSON array
// substring. Callers json.Unmarshal each in turn and use the first that
// succeeds — see internal/run.ParseDecomposedTasks for the canonical
// pattern this generalizes.
func Candidates(raw string) []string {
	candidates := []string{raw, StripCodeFence(raw)}
	if start, end, ok := OutermostBrackets(raw); ok {
		candidates = append(candidates, raw[start:end])
	}
	return candidates
}
