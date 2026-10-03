package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/eduardosanmartin/forge/internal/perms"
)

func TestHeadTailKeepsBothEndsOnRuneBoundaries(t *testing.T) {
	// "ñ" is 2 bytes: a naive byte cut lands mid-rune.
	out := []byte("START" + strings.Repeat("ñ", 20000) + "THE REAL ERROR")
	got, truncated := headTail(out, 101, 201)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !utf8.Valid(got) {
		t.Fatal("headTail produced invalid UTF-8")
	}
	s := string(got)
	if !strings.HasPrefix(s, "START") || !strings.HasSuffix(s, "THE REAL ERROR") {
		t.Fatalf("lost the head or the tail: %q...%q", s[:10], s[len(s)-20:])
	}
	if !strings.Contains(s, "bytes omitted") {
		t.Fatal("missing the omission marker")
	}
}

func TestHeadTailLeavesShortOutputAlone(t *testing.T) {
	out := []byte("short")
	if got, truncated := headTail(out, 4096, 8192); truncated || string(got) != "short" {
		t.Fatalf("short output changed: %q truncated=%v", got, truncated)
	}
}

// RNF-2.5 regression: fs_read used to return whole files into the context.
func TestFsReadDefaultCapPagesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	content := strings.Repeat("é", 3*DefaultReadLimitBytes) // 2-byte runes, 96 KB
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newFsReadTool()
	res, err := tool.Execute(context.Background(), perms.Request{Kind: perms.KindFsRead, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) > DefaultReadLimitBytes+200 {
		t.Fatalf("content = %d bytes, want <= cap (%d) + note", len(res.Content), DefaultReadLimitBytes)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatal("page cut a UTF-8 character in half")
	}
	if trunc, _ := res.Metadata["truncated"].(bool); !trunc {
		t.Fatal("metadata should flag truncation")
	}
	next, _ := res.Metadata["next_offset"].(int64)
	if next <= 0 || !strings.Contains(res.Content, "offset=") {
		t.Fatalf("missing continuation hint (next_offset=%v)", res.Metadata["next_offset"])
	}

	res2, err := tool.Execute(context.Background(), perms.Request{Kind: perms.KindFsRead, Path: path, Offset: next})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res2.Content, "é") {
		t.Fatal("second page should continue exactly where the first stopped")
	}
}
