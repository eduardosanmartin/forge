package pathmatch

import (
	"strings"
	"testing"
)

func TestValidatePatternRejects(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr string
	}{
		{name: "empty pattern", pattern: "", wantErr: "empty"},
		{name: "dotdot segment", pattern: "a/../b", wantErr: ".."},
		{name: "bare dotdot", pattern: "..", wantErr: ".."},
		{name: "backslash separator", pattern: `a\b`, wantErr: "backslash"},
		{name: "empty middle segment", pattern: "a//b", wantErr: "empty segment"},
		{name: "trailing double slash", pattern: "src//", wantErr: "empty segment"},
		{name: "bare root slash", pattern: "/", wantErr: "no segments"},
		{name: "bare dot slash", pattern: "./", wantErr: "no segments"},
		{name: "stray dot segment", pattern: "src/./x", wantErr: `"."`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePattern(tc.pattern)
			if err == nil {
				t.Fatalf("ValidatePattern(%q) = nil, want error mentioning %q", tc.pattern, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidatePattern(%q) error = %q, want mention of %q", tc.pattern, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidatePatternAccepts(t *testing.T) {
	// Drive-letter forms are valid patterns on every platform: they are
	// matched only against forward-slashed strings, never opened on disk.
	cases := []string{
		"./**",
		"src/**",
		"*.go",
		"src/**/*.go",
		"a/b/c.txt",
		"dir/",           // trailing slash: directory-prefix semantics
		"/abs/rooted",    // POSIX absolute
		"/abs/nested/**", // POSIX absolute with doublestar
		"C:/Users/dev/project/**",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if err := ValidatePattern(p); err != nil {
				t.Errorf("ValidatePattern(%q) = %v, want nil", p, err)
			}
		})
	}
}

func TestIsAbsoluteClassification(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"/etc/passwd", true},
		{"C:/Users/x", true},
		{"c:/users/x", true},
		{"C:", true},
		{"./src", false},
		{"src/**", false},
		{"a:b/c", false}, // colon not in drive position stays relative
		{"*.go", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := IsAbsolute(tc.pattern); got != tc.want {
				t.Errorf("IsAbsolute(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestMatchTable(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{
			name:    "doublestar matches nested file",
			pattern: "src/**",
			path:    "src/a/b/c.go",
			want:    true,
		},
		{
			name:    "doublestar matches direct child",
			pattern: "src/**",
			path:    "src/f.txt",
			want:    true,
		},
		{
			// Pinned decision: naive zero-or-more would let "src/**" cover
			// "src" itself; forge requires descent into the directory.
			name:    "doublestar does NOT match the directory itself",
			pattern: "src/**",
			path:    "src",
			want:    false,
		},
		{
			name:    "bare doublestar matches everything",
			pattern: "**",
			path:    "a/b/c/deep.txt",
			want:    true,
		},
		{
			name:    "bare doublestar matches top-level file",
			pattern: "**",
			path:    "main.go",
			want:    true,
		},
		{
			name:    "leading doublestar consumes zero segments",
			pattern: "**/*.go",
			path:    "main.go",
			want:    true,
		},
		{
			name:    "leading doublestar spans directories",
			pattern: "**/*.go",
			path:    "a/b/main.go",
			want:    true,
		},
		{
			name:    "single star stays within one segment",
			pattern: "*.go",
			path:    "t.go",
			want:    true,
		},
		{
			name:    "single star never crosses slashes",
			pattern: "*.go",
			path:    "sub/t.go",
			want:    false,
		},
		{
			name:    "exact file hit",
			pattern: "go.mod",
			path:    "go.mod",
			want:    true,
		},
		{
			name:    "nested exact hit",
			pattern: "cmd/forge/main.go",
			path:    "cmd/forge/main.go",
			want:    true,
		},
		{
			name:    "exact miss on different name",
			pattern: "cmd/forge/main.go",
			path:    "cmd/forge/other.go",
			want:    false,
		},
		{
			name:    "trailing slash equals dir doublestar hit",
			pattern: "build/",
			path:    "build/out.o",
			want:    true,
		},
		{
			name:    "trailing slash does not match dir itself",
			pattern: "build/",
			path:    "build",
			want:    false,
		},
		{
			name:    "./ prefix stripped from pattern",
			pattern: "./src/app.go",
			path:    "src/app.go",
			want:    true,
		},
		{
			name:    "case-sensitive literal mismatch",
			pattern: "README.md",
			path:    "readme.md",
			want:    false,
		},
		{
			name:    "middle doublestar allows zero middle segments",
			pattern: "a/**/b",
			path:    "a/b",
			want:    true,
		},
		{
			name:    "middle doublestar spans middle segments",
			pattern: "a/**/b",
			path:    "a/x/y/b",
			want:    true,
		},
		{
			name:    "empty pattern never matches",
			pattern: "",
			path:    "anything",
			want:    false,
		},
		{
			name:    "invalid pattern never matches",
			pattern: "../escape",
			path:    "escape",
			want:    false,
		},
		{
			name:    "longer path than pattern misses",
			pattern: "src",
			path:    "src/extra/deep",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.pattern, tc.path); got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}
