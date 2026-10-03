package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func shortPathName(t *testing.T, long string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// CI regression (windows-latest, 2026-10-03): with a temp dir spelled in
// 8.3 short form (C:\Users\RUNNER~1\...), worktree paths inside the repo
// were rejected as "outside the repository" because git reports the long
// form. Reproduced locally by pointing TMP at a short name.
func TestCanonicalPathExpandsShortNames(t *testing.T) {
	long := filepath.Join(t.TempDir(), "AVeryLongDirectoryNameForShortPathTest")
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatal(err)
	}
	short := shortPathName(t, long)
	if short == "" || strings.EqualFold(short, long) {
		t.Skip("8.3 short names not available on this volume")
	}
	notYet := filepath.Join(short, "wt-task-1")
	if got, want := canonicalPath(notYet), filepath.Join(canonicalPath(long), "wt-task-1"); !strings.EqualFold(got, want) {
		t.Fatalf("canonicalPath(%q) = %q, want %q", notYet, got, want)
	}
}
