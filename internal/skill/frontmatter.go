package skill

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// fieldVal holds a parsed frontmatter value with its source line.
type fieldVal struct {
	line int
	raw  string
	kind string // "string","bool","array"
	str  string
	bval bool
	arr  []string
}

// parseSkillFile parses a SKILL.md file's frontmatter and body.
// dirPath is the skill directory (used for DirPath field and error messages).
// The file MUST start with a "---" fence, contain key: value pairs, and close
// with "---"; everything after is the instructions body (verbatim).
func parseSkillFile(data []byte, dirPath string) (Skill, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return Skill{}, fmt.Errorf("skill line 1: unexpected UTF-8 BOM")
	}

	text := string(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, fmt.Errorf("skill line 1: missing opening frontmatter fence '---'")
	}

	allowedKeys := map[string]bool{
		"name":                true,
		"description":         true,
		"category":            true,
		"source":              true,
		"checksum":            true,
		"activation_keywords": true,
		"scripts":             true,
	}

	fields := make(map[string]fieldVal)
	bodyStartLine := -1

	lineErr := func(line int, format string, args ...any) error {
		return fmt.Errorf("skill line %d: "+format, append([]any{line}, args...)...)
	}

	// Parse frontmatter lines between opening fence (line 1) and closing fence.
	foundClose := false
	for idx := 1; idx < len(lines); idx++ {
		ln := idx + 1
		rawLine := lines[idx]

		// BOM check already done at file start; but mid-file BOM is also error? ignore.

		// Reject tab indentation anywhere in frontmatter.
		if strings.Contains(rawLine, "\t") {
			return Skill{}, lineErr(ln, "tabs are not allowed")
		}

		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "---" {
			foundClose = true
			bodyStartLine = idx + 1
			break
		}
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Reject block scalars and nested mappings helpers.
		// Block scalar indicators after colon: "|", ">" on same line or next indent.
		// We detect them by checking if after colon the value starts with | or >.
		// Anchors/aliases/tags.
		if strings.Contains(trimmed, "&") || strings.Contains(trimmed, "*") {
			// Check outside strings - crude but strict: if contains & or * we reject as anchors/aliases.
			// Allow them inside quoted strings? We'll check outside string.
			if containsAnchorOutsideString(trimmed) {
				return Skill{}, lineErr(ln, "anchors and aliases are not supported")
			}
		}
		if strings.HasPrefix(trimmed, "!") || strings.Contains(trimmed, " !") {
			// Tag indicator.
			if isTagOutsideString(trimmed) {
				return Skill{}, lineErr(ln, "tags are not supported")
			}
		}

		// Strip trailing comment outside strings.
		stripped := stripYAMLComment(rawLine)
		strippedTrim := strings.TrimSpace(stripped)
		if strippedTrim == "" {
			continue
		}
		if strippedTrim == "---" {
			foundClose = true
			bodyStartLine = idx + 1
			break
		}

		// Detect nested mapping: line without value but with colon suggests mapping?
		// In YAML frontmatter, a line like "metadata:" with no value and indented children would be nested.
		// We reject any line where after colon the value is empty and line is not a known key with empty? Simpler: if value part is empty, we error as missing value which also covers nested.
		colonIdx := findColonOutsideString(strippedTrim)
		if colonIdx < 0 {
			return Skill{}, lineErr(ln, "missing ':' in key-value pair")
		}
		keyPart := strings.TrimSpace(strippedTrim[:colonIdx])
		valPart := strings.TrimSpace(strippedTrim[colonIdx+1:])

		if keyPart == "" {
			return Skill{}, lineErr(ln, "empty key")
		}
		if strings.Contains(keyPart, ".") {
			return Skill{}, lineErr(ln, "dotted keys are not supported: %q", keyPart)
		}
		if !isBareKey(keyPart) {
			return Skill{}, lineErr(ln, "invalid bare key %q", keyPart)
		}
		if !allowedKeys[keyPart] {
			return Skill{}, lineErr(ln, "unknown field %q", keyPart)
		}
		if _, exists := fields[keyPart]; exists {
			return Skill{}, lineErr(ln, "duplicate field %q", keyPart)
		}
		if valPart == "" {
			// Could be a nested mapping start like "metadata:" with no inline value.
			// Either way, we reject as missing value and also as nested mapping.
			return Skill{}, lineErr(ln, "missing value for key %q (nested mappings are not supported)", keyPart)
		}

		// Reject block scalars: value starts with | or > .
		if valPart == "|" || valPart == ">" || strings.HasPrefix(valPart, "| ") || strings.HasPrefix(valPart, "> ") || strings.HasPrefix(valPart, "|#") || strings.HasPrefix(valPart, ">#") {
			return Skill{}, lineErr(ln, "block scalars are not supported")
		}
		// Also detect "|", ">" as first char.
		if len(valPart) > 0 && (valPart[0] == '|' || valPart[0] == '>') {
			return Skill{}, lineErr(ln, "block scalars are not supported")
		}

		// Reject inline tables (YAML flow mapping) like {a: b}
		if strings.HasPrefix(valPart, "{") {
			return Skill{}, lineErr(ln, "inline tables are not supported")
		}
		// Reject nested mapping indicator inside value? For simplicity, we already handle.

		fv, err := parseYAMLValue(valPart, ln)
		if err != nil {
			if strings.Contains(err.Error(), "skill line") {
				return Skill{}, err
			}
			return Skill{}, lineErr(ln, "%v", err)
		}

		// Additional strict: if the original line after colon had trailing content that looks like nested? Already parsed.
		fields[keyPart] = fv
	}

	if !foundClose {
		return Skill{}, fmt.Errorf("skill line %d: missing closing frontmatter fence '---'", len(lines))
	}

	// Build Skill from fields.
	var sk Skill
	sk.DirPath = dirPath

	// Helper to extract.
	getString := func(key string) (string, bool) {
		fv, ok := fields[key]
		if !ok {
			return "", false
		}
		if fv.kind != "string" {
			// Type mismatch: record as error via buildErrs below.
			return "", true
		}
		return fv.str, true
	}
	getArray := func(key string) ([]string, bool) {
		fv, ok := fields[key]
		if !ok {
			return nil, false
		}
		if fv.kind != "array" {
			return nil, true
		}
		return fv.arr, true
	}

	var buildErrs []error

	// name
	if fv, ok := fields["name"]; ok {
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be a string", fv.line, "name"))
		} else {
			sk.Name = fv.str
		}
	}
	// description
	if fv, ok := fields["description"]; ok {
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be a string", fv.line, "description"))
		} else {
			sk.Description = fv.str
		}
	}
	// category (optional)
	if fv, ok := fields["category"]; ok {
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be a string", fv.line, "category"))
		} else {
			sk.Category = fv.str
		}
	}
	// source
	if fv, ok := fields["source"]; ok {
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be a string", fv.line, "source"))
		} else {
			sk.Source = Source(fv.str)
		}
	}
	// checksum
	if fv, ok := fields["checksum"]; ok {
		if fv.kind != "string" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be a string", fv.line, "checksum"))
		} else {
			sk.Checksum = fv.str
		}
	}
	// activation_keywords
	if fv, ok := fields["activation_keywords"]; ok {
		if fv.kind != "array" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be an array of strings", fv.line, "activation_keywords"))
		} else {
			sk.ActivationKeywords = fv.arr
		}
	} else {
		sk.ActivationKeywords = nil
	}
	// scripts
	if fv, ok := fields["scripts"]; ok {
		if fv.kind != "array" {
			buildErrs = append(buildErrs, fmt.Errorf("skill line %d: field %q must be an array of strings", fv.line, "scripts"))
		} else {
			sk.Scripts = fv.arr
		}
	} else {
		sk.Scripts = nil
	}

	if len(buildErrs) > 0 {
		return Skill{}, errors.Join(buildErrs...)
	}

	_ = getString
	_ = getArray

	// Body is everything after closing fence, verbatim (join with \n).
	if bodyStartLine >= 0 && bodyStartLine < len(lines) {
		bodyLines := lines[bodyStartLine:]
		// Preserve original newlines: join with \n.
		sk.Instructions = strings.Join(bodyLines, "\n")
		// Trim leading newline that corresponds to fence line break? Keep as is but trim one leading newline if present due to empty first line after fence.
		// The split above includes line after fence; join preserves. We want verbatim.
		// Remove trailing whitespace? Keep verbatim.
	} else {
		sk.Instructions = ""
	}

	return sk, nil
}

// stripYAMLComment removes trailing '#' comment outside strings.
func stripYAMLComment(rawLine string) string {
	var sb strings.Builder
	inString := false
	escape := false
	quoteChar := rune(0)
	for _, r := range rawLine {
		if inString {
			sb.WriteRune(r)
			if escape {
				escape = false
				continue
			}
			if r == '\\' && quoteChar == '"' {
				escape = true
				continue
			}
			if r == quoteChar {
				inString = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = true
			quoteChar = r
			sb.WriteRune(r)
			continue
		}
		if r == '#' {
			break
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// findColonOutsideString finds first ':' outside quoted strings.
func findColonOutsideString(s string) int {
	inString := false
	escape := false
	quoteChar := rune(0)
	for i, r := range s {
		if inString {
			if escape {
				escape = false
				continue
			}
			if r == '\\' && quoteChar == '"' {
				escape = true
				continue
			}
			if r == quoteChar {
				inString = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = true
			quoteChar = r
			continue
		}
		if r == ':' {
			return i
		}
	}
	return -1
}

// containsAnchorOutsideString reports whether s contains & or * outside strings.
func containsAnchorOutsideString(s string) bool {
	inString := false
	escape := false
	quoteChar := rune(0)
	for _, r := range s {
		if inString {
			if escape {
				escape = false
				continue
			}
			if r == '\\' && quoteChar == '"' {
				escape = true
				continue
			}
			if r == quoteChar {
				inString = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = true
			quoteChar = r
			continue
		}
		if r == '&' || r == '*' {
			return true
		}
	}
	return false
}

// isTagOutsideString checks for tags outside strings.
func isTagOutsideString(s string) bool {
	inString := false
	escape := false
	quoteChar := rune(0)
	for i, r := range s {
		if inString {
			if escape {
				escape = false
				continue
			}
			if r == '\\' && quoteChar == '"' {
				escape = true
				continue
			}
			if r == quoteChar {
				inString = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = true
			quoteChar = r
			continue
		}
		if r == '!' {
			// ! at start or after space indicates tag.
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return true
			}
		}
	}
	return false
}

// isBareKey reports whether s matches [A-Za-z0-9_-]+ .
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

// parseYAMLValue parses a single YAML value on one line.
func parseYAMLValue(val string, line int) (fieldVal, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return fieldVal{}, fmt.Errorf("skill line %d: missing value", line)
	}
	// Quoted string (double or single)
	if strings.HasPrefix(val, `"`) {
		str, err := parseQuotedString(val, line, '"')
		if err != nil {
			return fieldVal{}, err
		}
		return fieldVal{line: line, raw: val, kind: "string", str: str}, nil
	}
	if strings.HasPrefix(val, `'`) {
		str, err := parseQuotedString(val, line, '\'')
		if err != nil {
			return fieldVal{}, err
		}
		return fieldVal{line: line, raw: val, kind: "string", str: str}, nil
	}
	// Inline array
	if strings.HasPrefix(val, "[") {
		arr, err := parseInlineArray(val, line)
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
	// Unquoted string: take as string verbatim.
	// Reject if it contains characters that suggest unsupported YAML features.
	if strings.Contains(val, "{") || strings.Contains(val, "}") {
		return fieldVal{}, fmt.Errorf("skill line %d: inline tables are not supported", line)
	}
	// Validate unquoted string doesn't look like an array/object.
	// Accept as string.
	return fieldVal{line: line, raw: val, kind: "string", str: val}, nil
}

// parseQuotedString parses a quoted string value.
func parseQuotedString(val string, line int, quote rune) (string, error) {
	if len(val) < 2 || rune(val[0]) != quote {
		return "", fmt.Errorf("skill line %d: invalid string %q", line, val)
	}
	var sb strings.Builder
	inEscape := false
	closed := false
	// Use runes iteration but handle bytes for escapes; for simplicity use bytes for double quotes.
	if quote == '"' {
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
					return "", fmt.Errorf("skill line %d: unknown escape \\%c in string", line, c)
				}
				inEscape = false
				continue
			}
			if c == '\\' {
				inEscape = true
				continue
			}
			if c == '"' {
				if i != len(val)-1 {
					return "", fmt.Errorf("skill line %d: unexpected content after string", line)
				}
				closed = true
				break
			}
			if c == '\n' || c == '\r' {
				return "", fmt.Errorf("skill line %d: unterminated string", line)
			}
			sb.WriteByte(c)
		}
		if inEscape {
			return "", fmt.Errorf("skill line %d: trailing escape in string", line)
		}
		if !closed {
			return "", fmt.Errorf("skill line %d: unterminated string", line)
		}
		return sb.String(), nil
	}
	// Single quoted: '' escapes by doubling '' (YAML single quote). Simplified: no escapes except ''.
	for i := 1; i < len(val); i++ {
		c := val[i]
		if c == '\'' {
			if i+1 < len(val) && val[i+1] == '\'' {
				sb.WriteByte('\'')
				i++
				continue
			}
			if i != len(val)-1 {
				return "", fmt.Errorf("skill line %d: unexpected content after string", line)
			}
			closed = true
			break
		}
		sb.WriteByte(c)
	}
	if !closed {
		return "", fmt.Errorf("skill line %d: unterminated string", line)
	}
	return sb.String(), nil
}

// parseInlineArray parses a single-line array of strings like ["a", "b"].
func parseInlineArray(val string, line int) ([]string, error) {
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		if strings.HasPrefix(val, "[") && !strings.HasSuffix(val, "]") {
			return nil, fmt.Errorf("skill line %d: multi-line arrays are not supported", line)
		}
		return nil, fmt.Errorf("skill line %d: invalid array %q", line, val)
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return []string{}, nil
	}
	var out []string
	inString := false
	escape := false
	var cur strings.Builder
	hasElement := false
	quoteChar := rune(0)

	flush := func() error {
		elem := strings.TrimSpace(cur.String())
		cur.Reset()
		if elem == "" {
			return fmt.Errorf("skill line %d: empty array element", line)
		}
		if len(elem) == 0 {
			return fmt.Errorf("skill line %d: empty array element", line)
		}
		// Expect string element: either quoted or unquoted.
		// For strictness, require quoted strings inside arrays (like plugin). But we allow unquoted as well for ergonomics.
		var str string
		if elem[0] == '"' {
			s, err := parseQuotedString(elem, line, '"')
			if err != nil {
				return err
			}
			str = s
		} else if elem[0] == '\'' {
			s, err := parseQuotedString(elem, line, '\'')
			if err != nil {
				return err
			}
			str = s
		} else {
			// Unquoted element inside array: treat as string, but reject bare special chars.
			if strings.Contains(elem, "{") || strings.Contains(elem, "}") {
				return fmt.Errorf("skill line %d: array elements must be strings", line)
			}
			// Reject if contains colon etc? For array of strings we accept plain.
			if elem == "true" || elem == "false" {
				return fmt.Errorf("skill line %d: array elements must be strings", line)
			}
			str = elem
		}
		out = append(out, str)
		hasElement = false
		return nil
	}

	for _, r := range inner {
		if inString {
			cur.WriteRune(r)
			if escape {
				escape = false
				continue
			}
			if r == '\\' && quoteChar == '"' {
				escape = true
				continue
			}
			if r == quoteChar {
				// For single quote, '' handling already? We'll just close.
				inString = false
			}
			continue
		}
		if r == '"' || r == '\'' {
			inString = true
			quoteChar = r
			hasElement = true
			cur.WriteRune(r)
			continue
		}
		if r == ',' {
			if !hasElement {
				return nil, fmt.Errorf("skill line %d: empty array element", line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if unicode.IsSpace(r) {
			if hasElement {
				cur.WriteRune(r)
			}
			continue
		}
		// Regular char outside string: part of unquoted element.
		if !hasElement {
			hasElement = true
		}
		cur.WriteRune(r)
		// Detect unsupported '#' inside array outside string.
		if r == '#' {
			return nil, fmt.Errorf("skill line %d: unexpected '#' in array", line)
		}
	}

	if inString {
		return nil, fmt.Errorf("skill line %d: unterminated string in array", line)
	}
	if hasElement {
		if err := flush(); err != nil {
			return nil, err
		}
	} else {
		trimmedInner := strings.TrimSpace(inner)
		if strings.HasSuffix(trimmedInner, ",") {
			return nil, fmt.Errorf("skill line %d: trailing comma in array", line)
		}
	}
	return out, nil
}

// contains helper for tests internal.
func stringContains(s, substr string) bool {
	return strings.Contains(s, substr)
}
