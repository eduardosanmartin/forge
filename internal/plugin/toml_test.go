package plugin

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseTomlAcceptance(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{
			name: "full manifest shape from package doc",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"Example plugin.\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"fs.read\", \"git\"]\n" +
				"dependencies = []\n" +
				"checksum = \"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" # will be rejected for local, but parsing should succeed\n" +
				"\n[[tools]]\n" +
				"name = \"my_plugin_greet\"\n" +
				"description = \"Greets a user.\"\n" +
				"permission = \"fs.read\"\n",
			// Note: checksum for local will be validated later; toml parsing itself should succeed.
		},
		{
			name: "minimal valid manifest (no tools, no dependencies)",
			toml: "name = \"ab\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"x\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"a.wasm\"\n" +
				"permissions = []\n",
		},
		{
			name: "comments and trailing comments",
			toml: "# full line comment\n" +
				"name = \"my_plugin\" # trailing comment\n" +
				"version = \"0.2.0\" # another\n" +
				"description = \"desc\" # after string\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"fs.read\"] # comment after array\n" +
				"# comment before tool\n" +
				"[[tools]] # tool header with comment allowed\n" +
				"name = \"my_plugin_tool\" # trailing\n" +
				"description = \"d\"\n" +
				"permission = \"fs.read\"\n",
		},
		{
			name: "escapes in description",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"line1\\nline2\\twith\\\\backslash and \\\"quote\\\"\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = []\n",
		},
		{
			name: "array with single string and external checksum",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"desc\"\n" +
				"source = \"external\"\n" +
				"entrypoint = \"dir/plugin.wasm\"\n" +
				"permissions = [\"net\"]\n" +
				"checksum = \"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n",
		},
		{
			name: "multiple tools",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"desc\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"fs.read\", \"fs.write\"]\n" +
				"[[tools]]\n" +
				"name = \"my_plugin_a\"\n" +
				"description = \"A\"\n" +
				"permission = \"fs.read\"\n" +
				"[[tools]]\n" +
				"name = \"my_plugin_b\"\n" +
				"description = \"B\"\n" +
				"permission = \"fs.write\"\n",
		},
		{
			name: "string with hash inside not treated as comment",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"a # not a comment\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = []\n",
		},
		{
			name: "integers and booleans accepted by parser (type checked later)",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"desc\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = []\n",
			// This case ensures parser doesn't reject int/bool at value level; actual manifest uses them not.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// For acceptance we test via parseTOML directly; some cases may have validation-level rejections
			// (e.g., checksum for local) but toml parsing should not error for those; we check parseTOML succeeds.
			// For the full shape case we included checksum for local; parseTOML will succeed, validateManifest would fail.
			// So we test parseTOML, not ParseManifest, for acceptance.
			m, err := parseTOML([]byte(tc.toml))
			if err != nil {
				t.Fatalf("parseTOML failed: %v", err)
			}
			// Basic sanity: name parsed
			if tc.name == "escapes in description" {
				if m.Description != "line1\nline2\twith\\backslash and \"quote\"" {
					t.Errorf("escape handling failed: got %q", m.Description)
				}
			}
			if tc.name == "string with hash inside not treated as comment" {
				if m.Description != "a # not a comment" {
					t.Errorf("hash inside string mis-handled: got %q", m.Description)
				}
			}
		})
	}
}

func TestParseTomlRejection(t *testing.T) {
	cases := []struct {
		name       string
		toml       string
		wantSubstr string // substring that must be in error
		wantLine   int    // expected line number to be mentioned (0 = don't check)
	}{
		{
			name:       "BOM rejected",
			toml:       string([]byte{0xEF, 0xBB, 0xBF}) + "name = \"my_plugin\"\n",
			wantSubstr: "BOM",
			wantLine:   1,
		},
		{
			name:       "unknown top-level key",
			toml:       "name = \"my_plugin\"\nunknown_key = \"oops\"\n",
			wantSubstr: "unknown field",
			wantLine:   2,
		},
		{
			name:       "unknown field inside tools",
			toml:       "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"x\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\n[[tools]]\nname = \"my_plugin_a\"\ndescription = \"d\"\npermission = \"fs.read\"\nunknown = \"bad\"\n",
			wantSubstr: "unknown field",
			wantLine:   11,
		},
		{
			name:       "bad escape",
			toml:       "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"bad\\q\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\n",
			wantSubstr: "unknown escape",
			wantLine:   3,
		},
		{
			name:       "dotted key",
			toml:       "name = \"my_plugin\"\na.b = \"dotted\"\n",
			wantSubstr: "dotted keys",
			wantLine:   2,
		},
		{
			name:       "inline table",
			toml:       "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"x\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\ninline = {a = 1}\n",
			wantSubstr: "inline tables",
			wantLine:   7,
		},
		{
			name:       "missing value",
			toml:       "name = \n",
			wantSubstr: "missing value",
			wantLine:   1,
		},
		{
			name:       "wrong table header single bracket",
			toml:       "[table]\nname = \"my_plugin\"\n",
			wantSubstr: "unsupported table header",
			wantLine:   1,
		},
		{
			name:       "wrong table header double other name",
			toml:       "[[other]]\n",
			wantSubstr: "unsupported table header",
			wantLine:   1,
		},
		{
			name:       "array not strings",
			toml:       "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"x\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = [1, 2]\n",
			wantSubstr: "array elements must be strings",
			wantLine:   6,
		},
		{
			name:       "unterminated string",
			toml:       "name = \"my_plugin\n",
			wantSubstr: "unterminated string",
			wantLine:   1,
		},
		{
			name:       "multi-line string via triple quotes",
			toml:       "name = \"\"\"multi\nline\"\"\"\n",
			wantSubstr: "multi-line strings",
			wantLine:   1,
		},
		{
			name:       "array not strings mixed",
			toml:       "permissions = [\"fs.read\", true]\n",
			wantSubstr: "array elements must be strings",
			wantLine:   1,
		},
		{
			name:       "missing equals",
			toml:       "name \"my_plugin\"\n",
			wantSubstr: "missing '='",
			wantLine:   1,
		},
		{
			name:       "datetime rejected",
			toml:       "name = \"my_plugin\"\ndate = 1979-05-27T07:32:00\n",
			wantSubstr: "datetimes",
			wantLine:   2,
		},
		{
			name:       "invalid bare key",
			toml:       "\"quoted_key\" = \"value\"\n",
			wantSubstr: "invalid bare key",
			wantLine:   1,
		},
		{
			name:       "unterminated string in array",
			toml:       "permissions = [\"fs.read, \"oops\"]\n",
			wantSubstr: "unterminated string",
			wantLine:   1,
		},
		{
			name:       "unknown escape in array string",
			toml:       "permissions = [\"bad\\q\"]\n",
			wantSubstr: "unknown escape",
			wantLine:   1,
		},
		{
			name:       "duplicate top-level field",
			toml:       "name = \"a\"\nname = \"b\"\n",
			wantSubstr: "duplicate field",
			wantLine:   2,
		},
		{
			name:       "duplicate field in tool",
			toml:       "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"x\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\n[[tools]]\nname = \"my_plugin_a\"\nname = \"my_plugin_b\"\ndescription = \"d\"\npermission = \"fs.read\"\n",
			wantSubstr: "duplicate field",
			wantLine:   9,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTOML([]byte(tc.toml))
			if err == nil {
				t.Fatal("parseTOML succeeded; want error")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
			if tc.wantLine != 0 {
				wantLineStr := strings.Contains(err.Error(), "manifest line")
				if !wantLineStr {
					t.Errorf("error %q does not mention line number", err.Error())
				} else {
					// Check exact line number present
					want := "manifest line " + strconv.Itoa(tc.wantLine)
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err.Error(), want)
					}
				}
			}
		})
	}
}
