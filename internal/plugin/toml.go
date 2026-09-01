package plugin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// fieldVal holds a parsed TOML value with its source line for error reporting.
type fieldVal struct {
	line int
	raw  string // raw value string after '=' (trimmed, comments stripped)
	kind string // "string","int","bool","array"
	str  string // for string kind
	ival int64
	bval bool
	arr  []string // for array of strings
}

// parseTOML parses the strict TOML subset for plugin manifests.
// Supported subset (all other TOML features are rejected):
//
//   - Full-line '#' comments and trailing comments after values (outside strings).
//   - Top-level bare keys ([a-z A-Z 0-9 _ -]), values: basic strings with escapes \\ \" \n \t (unknown escapes rejected), integers (decimal), booleans true|false, single-line arrays of strings.
//   - One section type: [[tools]] array-of-tables. Reject inline tables '{}', multi-line strings, datetimes, dotted keys, and any [table] header other than [[tools]].
//   - UTF-8 BOM at start is rejected.
//   - Unknown top-level keys or unknown keys inside [[tools]] cause an error (strict manifest).
//   - Errors include line numbers where possible as "manifest line N: ...".
func parseTOML(data []byte) (Manifest, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return Manifest{}, fmt.Errorf("manifest line 1: unexpected UTF-8 BOM")
	}

	// Split into lines preserving line numbers. Handle both \n and \r\n.
	text := string(data)
	// Normalize \r\n -> \n and standalone \r -> \n for line counting.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	// If original data ended with \n, Split produces trailing empty element; keep it for counting but ignore empty trailing.
	// We'll iterate with line number = index+1.

	// Known top-level keys.
	allowedTop := map[string]bool{
		"name":         true,
		"version":      true,
		"description":  true,
		"source":       true,
		"entrypoint":   true,
		"permissions":  true,
		"dependencies": true,
		"checksum":     true,
	}
	allowedTool := map[string]bool{
		"name":        true,
		"description": true,
		"permission":  true,
	}

	topFields := make(map[string]fieldVal)
	var tools []map[string]fieldVal // one entry per [[tools]]
	var currentTool map[string]fieldVal // nil until first [[tools]]
	inTools := false

	// Helper to report line error.
	lineErr := func(line int, format string, args ...any) error {
		return fmt.Errorf("manifest line %d: "+format, append([]any{line}, args...)...)
	}

	for idx, rawLine := range lines {
		ln := idx + 1
		// Empty trailing split from final newline: rawLine == "" and ln == len(lines) with original ending newline.
		// Process uniformly; empty lines will be skipped.

		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		// Full-line comment: after trimming leading spaces, starts with '#'.
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect table headers.
		// Need to check before comment stripping because header may have trailing comment.
		// Determine if line is a header: after trimming leading spaces, starts with '['.
		leadTrim := strings.TrimLeft(rawLine, " \t")
		if strings.HasPrefix(leadTrim, "[") {
			// Extract header ignoring trailing comment outside strings outside header.
			// For headers we can strip trailing comment after the header token.
			// First find header token.
			// Header must be exactly "[[tools]]" possibly with surrounding whitespace and trailing comment.
			// Any other bracket form is error.
			// Use comment-stripping that respects strings (strings unlikely in header but handle).
				stripped := stripTrailingComment(rawLine)
			// stripped is trimmed?
			h := strings.TrimSpace(stripped)
			if h == "[[tools]]" {
				// Start new tool table.
				newTool := make(map[string]fieldVal)
				tools = append(tools, newTool)
				currentTool = tools[len(tools)-1]
				inTools = true
				continue
			}
			// Reject multi-line string delimiters if they appear as header-like?
			// But generic header error.
			if strings.HasPrefix(h, "[[") {
				return Manifest{}, lineErr(ln, "unsupported table header %q: only [[tools]] is allowed", h)
			}
			if strings.HasPrefix(h, "[") {
				return Manifest{}, lineErr(ln, "unsupported table header %q: only [[tools]] is allowed", h)
			}
			// If not header-like but still starts with '[' after trimming, treat as error.
			return Manifest{}, lineErr(ln, "unsupported table header %q: only [[tools]] is allowed", h)
		}

		// Detect bare triple-quote multi-line strings anywhere on line (outside handling).
		if strings.Contains(rawLine, `"""`) || strings.Contains(rawLine, `'''`) {
			return Manifest{}, lineErr(ln, "multi-line strings are not supported")
		}

		// For key = value lines, strip trailing comment outside strings.
		stripped := stripTrailingComment(rawLine)
		// stripped may still be empty if line was only comment after stripping (already handled) but double-check.
		strippedTrim := strings.TrimSpace(stripped)
		if strippedTrim == "" {
			continue
		}

		// Find '=' separating key and value, outside strings.
		eqIdx := findEqualsOutsideString(strippedTrim)
		if eqIdx < 0 {
			return Manifest{}, lineErr(ln, "missing '=' in key-value pair")
		}
		keyPart := strings.TrimSpace(strippedTrim[:eqIdx])
		valPart := strings.TrimSpace(strippedTrim[eqIdx+1:])

		if keyPart == "" {
			return Manifest{}, lineErr(ln, "empty key")
		}

		// Validate bare key charset and dotted keys.
		if strings.Contains(keyPart, ".") {
			return Manifest{}, lineErr(ln, "dotted keys are not supported: %q", keyPart)
		}
		if !isBareKey(keyPart) {
			return Manifest{}, lineErr(ln, "invalid bare key %q", keyPart)
		}

		if valPart == "" {
			return Manifest{}, lineErr(ln, "missing value for key %q", keyPart)
		}

		// Reject inline tables.
		if strings.HasPrefix(strings.TrimSpace(valPart), "{") {
			return Manifest{}, lineErr(ln, "inline tables are not supported")
		}

		// Datetime rejection: simple heuristic - values containing 'T' with digits and '-' and ':' are datetimes, reject.
		// But we already support only string/int/bool/array; any other shape will hit unsupported value path.
		// Explicitly reject datetime-like patterns to give clearer error.
		if looksLikeDatetime(valPart) {
			return Manifest{}, lineErr(ln, "datetimes are not supported")
		}

		// Parse value.
		fv, err := parseValue(valPart, ln)
		if err != nil {
			// parseValue already includes line number; if not, wrap.
			if strings.Contains(err.Error(), "manifest line") {
				return Manifest{}, err
			}
			return Manifest{}, lineErr(ln, "%v", err)
		}

		// Enforce strict keys: unknown top-level or unknown inside tools.
		if inTools && currentTool != nil {
			// Inside a tool table: only allowed tool keys.
			if !allowedTool[keyPart] {
				return Manifest{}, lineErr(ln, "unknown field %q in [[tools]]", keyPart)
			}
			if _, exists := currentTool[keyPart]; exists {
				return Manifest{}, lineErr(ln, "duplicate field %q in [[tools]]", keyPart)
			}
			currentTool[keyPart] = fv
		} else {
			// Top-level.
			if !allowedTop[keyPart] {
				return Manifest{}, lineErr(ln, "unknown field %q", keyPart)
			}
			if _, exists := topFields[keyPart]; exists {
				return Manifest{}, lineErr(ln, "duplicate field %q", keyPart)
			}
			topFields[keyPart] = fv
		}
	}

	// Build Manifest from parsed fields.
	var m Manifest
	var buildErrs []error

	// Helper to get string field.
	getString := func(key string) (string, bool) {
		fv, ok := topFields[key]
		if !ok {
			return "", false
		}
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("manifest line %d: field %q must be a string", fv.line, key))
			return "", true // present but wrong type, don't treat as missing
		}
		return fv.str, true
	}
	getArray := func(key string) ([]string, bool) {
		fv, ok := topFields[key]
		if !ok {
			return nil, false
		}
		if fv.kind != "array" {
			buildErrs = append(buildErrs, fmt.Errorf("manifest line %d: field %q must be an array of strings", fv.line, key))
			return nil, true
		}
		return fv.arr, true
	}

	// Name
	if v, ok := getString("name"); ok {
		m.Name = v
	}
	// Version
	if v, ok := getString("version"); ok {
		m.Version = v
	}
	// Description
	if v, ok := getString("description"); ok {
		m.Description = v
	}
	// Source
	if v, ok := getString("source"); ok {
		m.Source = Source(v)
	}
	// Entrypoint
	if v, ok := getString("entrypoint"); ok {
		m.Entrypoint = v
	}
	// Permissions
	if arr, ok := getArray("permissions"); ok {
		m.Permissions = arr
	} else {
		// Check if field present but type error already recorded; otherwise keep nil.
		if _, present := topFields["permissions"]; !present {
			m.Permissions = nil
		}
	}
	// Dependencies
	if arr, ok := getArray("dependencies"); ok {
		m.Dependencies = arr
	}
	// Checksum
	if v, ok := getString("checksum"); ok {
		m.Checksum = v
	}

	// Tools: convert each raw tool map to ToolExport, detecting type mismatches.
	for i, raw := range tools {
		te := ToolExport{}
		// name
		if fv, ok := raw["name"]; ok {
			if fv.kind != "string" {
				buildErrs = append(buildErrs, fmt.Errorf("manifest line %d: field %q in [[tools]] must be a string", fv.line, "name"))
			} else {
				te.Name = fv.str
			}
		}
		// description
		if fv, ok := raw["description"]; ok {
			if fv.kind != "string" {
				buildErrs = append(buildErrs, fmt.Errorf("manifest line %d: field %q in [[tools]] must be a string", fv.line, "description"))
			} else {
				te.Description = fv.str
			}
		} else {
			// missing description will be caught in validation; keep empty for now.
		}
		// permission
		if fv, ok := raw["permission"]; ok {
			if fv.kind != "string" {
				buildErrs = append(buildErrs, fmt.Errorf("manifest line %d: field %q in [[tools]] must be a string", fv.line, "permission"))
			} else {
				te.Permission = fv.str
			}
		}
		// If we had type errors for this tool, still append partial to preserve length.
		m.Tools = append(m.Tools, te)
		_ = i
	}

	if len(buildErrs) > 0 {
		return Manifest{}, errors.Join(buildErrs...)
	}

	return m, nil
}

// stripTrailingComment removes a trailing '#' comment that is outside a string.
// It scans rawLine character by character tracking string and escape state.
// Returns the line truncated before the comment (if any), preserving content before comment.
func stripTrailingComment(rawLine string) string {
	var sb strings.Builder
	inString := false
	escape := false
	for _, r := range rawLine {
		if inString {
			sb.WriteRune(r)
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		// Not in string
		if r == '"' {
			inString = true
			sb.WriteRune(r)
			continue
		}
		if r == '#' {
			// Start of comment outside string: truncate.
			break
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// findEqualsOutsideString finds the first '=' that is outside a quoted string.
func findEqualsOutsideString(s string) int {
	inString := false
	escape := false
	for i, r := range s {
		if inString {
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		if r == '"' {
			inString = true
			continue
		}
		if r == '=' {
			return i
		}
	}
	return -1
}

// isBareKey reports whether s matches [A-Za-z0-9_-]+.
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// looksLikeDatetime is a heuristic to give a better error for datetime values.
func looksLikeDatetime(s string) bool {
	// Datetimes contain '-' and ':' and 'T', but we be conservative: if s contains "T" between digits.
	// Example: 1979-05-27T07:32:00, 2021-01-01, etc.
	// We return true if s matches a date-like pattern to report datetimes not supported rather than generic unsupported value.
	// Simple: contains at least two '-' and a 'T' or ':'.
	if strings.Contains(s, "T") && strings.Contains(s, "-") && strings.Contains(s, ":") {
		return true
	}
	// ISO date without time: YYYY-MM-DD
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		// Check digits around
		ok := true
		for i, c := range s[:10] {
			if i == 4 || i == 7 {
				continue
			}
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// parseValue parses a single TOML value (already trimmed, comments stripped) on one line.
//
// Supported: string, integer, boolean, array-of-strings.
// It returns a fieldVal with kind and parsed data.
func parseValue(val string, line int) (fieldVal, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return fieldVal{}, fmt.Errorf("manifest line %d: missing value", line)
	}
	// String
	if strings.HasPrefix(val, `"`) {
		str, err := parseStringLiteral(val, line)
		if err != nil {
			return fieldVal{}, err
		}
		return fieldVal{line: line, raw: val, kind: "string", str: str}, nil
	}
	// Array of strings
	if strings.HasPrefix(val, "[") {
		arr, err := parseArrayOfStrings(val, line)
		if err != nil {
			return fieldVal{}, err
		}
		return fieldVal{line: line, raw: val, kind: "array", arr: arr}, nil
	}
	// Boolean
	if val == "true" {
		return fieldVal{line: line, raw: val, kind: "bool", bval: true}, nil
	}
	if val == "false" {
		return fieldVal{line: line, raw: val, kind: "bool", bval: false}, nil
	}
	// Integer (decimal)
	if isIntegerLiteral(val) {
		iv, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fieldVal{}, fmt.Errorf("manifest line %d: invalid integer %q", line, val)
		}
		return fieldVal{line: line, raw: val, kind: "int", ival: iv}, nil
	}
	// Reject unterminated string case: starts with " but parseStringLiteral would have caught.
	// Other cases: unsupported.
	return fieldVal{}, fmt.Errorf("manifest line %d: unsupported value %q", line, val)
}

// isIntegerLiteral reports whether s is a decimal integer literal (optional leading -).
func isIntegerLiteral(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	// Ensure no leading spaces etc already trimmed.
	return true
}

// parseStringLiteral parses a basic string literal: quoted with " and supports escapes \\ \" \n \t.
// val is the full value string (should start with " and end with ").
func parseStringLiteral(val string, line int) (string, error) {
	if len(val) < 2 || val[0] != '"' {
		return "", fmt.Errorf("manifest line %d: invalid string %q", line, val)
	}
	// Find closing quote that is not escaped, and ensure nothing beyond it except whitespace (already trimmed value should be exactly the string).
	// But val may have been trimmed; if it starts with " and is a string, it must end with " and nothing after.
	var sb strings.Builder
	inEscape := false
	// We know val[0] == '"', start after it.
	closed := false
	for i := 1; i < len(val); i++ {
		c := val[i]
		if inEscape {
			switch c {
			case '\\', '"':
				sb.WriteByte(c)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				return "", fmt.Errorf("manifest line %d: unknown escape \\%c in string", line, c)
			}
			inEscape = false
			continue
		}
		if c == '\\' {
			inEscape = true
			continue
		}
		if c == '"' {
			// Closing quote: must be last character.
			if i != len(val)-1 {
				return "", fmt.Errorf("manifest line %d: unexpected content after string", line)
			}
			closed = true
			break
		}
		// Disallow raw newlines inside single-line string (should not happen as we per-line parse, but check).
		if c == '\n' || c == '\r' {
			return "", fmt.Errorf("manifest line %d: unterminated string", line)
		}
		// Basic string cannot contain bare non-printable? Allow any except quote/escape handled.
		sb.WriteByte(c)
	}
	if inEscape {
		return "", fmt.Errorf("manifest line %d: trailing escape in string", line)
	}
	if !closed {
		return "", fmt.Errorf("manifest line %d: unterminated string", line)
	}
	return sb.String(), nil
}

// parseArrayOfStrings parses a single-line array of strings like ["a", "b"].
func parseArrayOfStrings(val string, line int) ([]string, error) {
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		// Multi-line arrays are not supported; if it starts with [ but doesn't end with ] on same line.
		if strings.HasPrefix(val, "[") && !strings.HasSuffix(val, "]") {
			return nil, fmt.Errorf("manifest line %d: multi-line arrays are not supported", line)
		}
		return nil, fmt.Errorf("manifest line %d: invalid array %q", line, val)
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return []string{}, nil
	}
	// Parse comma-separated string literals outside.
	var out []string
	inString := false
	escape := false
	var cur strings.Builder
	hasElement := false // whether we have started an element

	// Helper to flush current element.
	flush := func() error {
		elem := strings.TrimSpace(cur.String())
		cur.Reset()
		if elem == "" {
			return fmt.Errorf("manifest line %d: empty array element", line)
		}
		// elem must be a string literal.
		str, err := parseStringLiteral(elem, line)
		if err != nil {
			return err
		}
		out = append(out, str)
		hasElement = false
		return nil
	}

	for i, r := range inner {
		_ = i
		if inString {
			cur.WriteRune(r)
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		// Not in string
		if r == '"' {
			inString = true
			hasElement = true
			cur.WriteRune(r)
			continue
		}
		if r == ',' {
			if inString {
				cur.WriteRune(r)
				continue
			}
			// Separator between elements.
			if !hasElement {
				return nil, fmt.Errorf("manifest line %d: empty array element", line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if r == '#' {
			// Should not have comment inside array without being in string, but our outer stripping already handled trailing comment outside array?
			// Inside array, # is not expected; treat as error unless part of element.
			// Since we are not in string, # would start a comment but array already closed; we consider it error.
			return nil, fmt.Errorf("manifest line %d: unexpected '#' in array", line)
		}
		// Whitespace outside strings is ignored for element boundaries but we keep it to allow trimming.
		// Non-string content outside strings is error.
		if unicode.IsSpace(r) {
			if hasElement {
				cur.WriteRune(r)
			}
			continue
		}
		// Any other char outside string is invalid for array-of-strings.
		if !hasElement {
			// Trying to start a non-string element.
			return nil, fmt.Errorf("manifest line %d: array elements must be strings", line)
		}
		cur.WriteRune(r)
	}

	if inString {
		return nil, fmt.Errorf("manifest line %d: unterminated string in array", line)
	}
	if hasElement {
		if err := flush(); err != nil {
			return nil, err
		}
	} else {
		// Check trailing comma: e.g., ["a",] would have hasElement false after flush but last char was ','.
		// Our loop would have flushed on ','; if inner ends with ',' then we would have just flushed and hasElement false, but trailing comma should be rejected?
		// Allow trailing comma? Spec doesn't clarify; we reject trailing comma for strictness.
		trimmedInner := strings.TrimSpace(inner)
		if strings.HasSuffix(trimmedInner, ",") {
			return nil, fmt.Errorf("manifest line %d: trailing comma in array", line)
		}
	}
	// Validate no inline tables etc inside array already caught via non-string check.
	return out, nil
}


