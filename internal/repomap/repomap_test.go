package repomap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	root := t.TempDir()
	writeFile(t, root, "core/store.go", `package core

// Store persists things.
type Store struct{ db int }

// Open opens a store.
func Open(path string) (*Store, error) { return &Store{}, nil }

func (s *Store) Save(key string, value []byte) error { return nil }

func helper() {}
`)
	writeFile(t, root, "cmd/main.go", "package main\n\nfunc main() { s, _ := core.Open(\"x\"); s.Save(\"k\", nil) }\n")
	writeFile(t, root, "web/app.ts", "export function renderPage(id: string): string {\n  return id\n}\nexport class Router {}\n")
	writeFile(t, root, "tools/gen.py", "class Generator:\n    pass\n\ndef build_all(paths):\n    return paths\n")
	writeFile(t, root, "node_modules/dep/index.js", "export function ignored() {}\n")
	return root
}

func TestRenderListsExportedSymbolsOnly(t *testing.T) {
	ix := New(fixture(t))
	m := ix.Render(1000)
	for _, want := range []string{"core/store.go", "type Store struct", "func Open(path string) (*Store, error)", "func (s *Store) Save(key string, value []byte) error", "export function renderPage(id: string): string", "class Generator", "def build_all(paths)"} {
		if !strings.Contains(m, want) {
			t.Errorf("map missing %q:\n%s", want, m)
		}
	}
	for _, bad := range []string{"helper", "ignored", "return &Store"} {
		if strings.Contains(m, bad) {
			t.Errorf("map must not contain %q (unexported / skipped dir / body):\n%s", bad, m)
		}
	}
}

func TestRenderRanksReferencedFilesFirstAndRespectsBudget(t *testing.T) {
	ix := New(fixture(t))
	m := ix.Render(1000)
	if strings.Index(m, "core/store.go") > strings.Index(m, "tools/gen.py") {
		t.Errorf("core/store.go (referenced by cmd/main.go) should rank above unreferenced files:\n%s", m)
	}
	if small := ix.Render(20); len(small) > 20*4+120 {
		t.Errorf("budget of 20 tokens produced %d chars", len(small))
	}
}

// RNF-2.2/2.4: editing a function body must not change the map (it is a
// prompt prefix).
func TestRenderStableAcrossBodyEdits(t *testing.T) {
	root := fixture(t)
	ix := New(root)
	before := ix.Render(1000)
	writeFile(t, root, "core/store.go", strings.Replace(mustRead(t, root, "core/store.go"), "return &Store{}, nil", "s := &Store{}\n\treturn s, nil", 1))
	ix.maxAge = 0
	if after := ix.Render(1000); after != before {
		t.Fatalf("map changed after a body-only edit:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestSearch(t *testing.T) {
	ix := New(fixture(t))
	got := ix.Search("save", 10)
	if len(got) != 1 || got[0].Path != "core/store.go" || got[0].Kind != "method" || got[0].Line == 0 {
		t.Fatalf("Search(save) = %+v", got)
	}
}

func mustRead(t *testing.T, root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
