// Package repomap builds a compact map of a workspace's code — files and
// their top-level symbol signatures — so the model can orient itself
// without spending iterations and tokens on fs_list/fs_read exploration
// (F5). Go is parsed with go/parser; other languages use line-based
// patterns (good enough for orientation, not a full parser).
//
// The rendered map is deterministic and contains signatures only (no line
// numbers, no bodies), ranked globally by how often other files reference
// a file's symbols — NOT by the current request — so editing a function
// body doesn't change it and it stays a cacheable prompt prefix
// (RNF-2.2/2.4). Targeted lookups go through the code_symbols tool.
package repomap

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Symbol is one top-level declaration.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // func, method, type, class, const, var...
	Signature string `json:"signature"`
	Line      int    `json:"line"`
}

// File is one indexed source file.
type File struct {
	Path    string   `json:"path"` // workspace-relative, forward slashes
	Symbols []Symbol `json:"symbols"`
	refs    int      // references to this file's symbols from other files
	modTime time.Time
	size    int64
	idents  map[string]int
}

// Index is the symbol index of one workspace, refreshed incrementally.
type Index struct {
	root string

	mu      sync.Mutex
	files   map[string]*File
	built   time.Time
	maxAge  time.Duration
	maxSize int64
}

// New returns an index of root; nothing is scanned until first use.
func New(root string) *Index {
	return &Index{root: root, files: map[string]*File{}, maxAge: 30 * time.Second, maxSize: 512 * 1024}
}

var skipDirs = map[string]bool{".git": true, ".forge": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "__pycache__": true, ".venv": true}

var sourceExt = map[string]bool{".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".rs": true, ".java": true, ".cs": true, ".rb": true, ".php": true, ".kt": true, ".swift": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true}

// listFiles prefers `git ls-files` (honors .gitignore), else walks.
func (ix *Index) listFiles() []string {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = ix.root
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && sourceExt[strings.ToLower(filepath.Ext(l))] && !underSkipped(l) && !isTestFile(l) {
				files = append(files, l)
			}
		}
		return files
	}
	var files []string
	_ = filepath.WalkDir(ix.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != ix.root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExt[strings.ToLower(filepath.Ext(p))] && !isTestFile(p) {
			if rel, err := filepath.Rel(ix.root, p); err == nil {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return files
}

// isTestFile skips test sources: the map is for orienting in the code
// under change, and tests double the noise.
func isTestFile(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_")
}

func underSkipped(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

// Refresh re-indexes changed files (by mtime+size) at most every maxAge.
func (ix *Index) Refresh() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.built.IsZero() && time.Since(ix.built) < ix.maxAge {
		return
	}
	seen := map[string]bool{}
	for _, rel := range ix.listFiles() {
		seen[rel] = true
		info, err := os.Stat(filepath.Join(ix.root, filepath.FromSlash(rel)))
		if err != nil || info.Size() > ix.maxSize {
			delete(ix.files, rel)
			continue
		}
		if f, ok := ix.files[rel]; ok && f.modTime.Equal(info.ModTime()) && f.size == info.Size() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ix.root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		ix.files[rel] = &File{Path: rel, Symbols: extract(rel, data), modTime: info.ModTime(), size: info.Size(), idents: identCounts(data)}
	}
	for rel := range ix.files {
		if !seen[rel] {
			delete(ix.files, rel)
		}
	}
	ix.computeRefs()
	ix.built = time.Now()
}

// computeRefs counts, for each file, how many OTHER files mention its
// symbols — a cheap proxy for "central" code.
func (ix *Index) computeRefs() {
	// Names that appear in a large share of files (Name, String, Execute…)
	// say nothing about centrality; skip them.
	df := map[string]int{}
	for _, f := range ix.files {
		for id := range f.idents {
			df[id]++
		}
	}
	common := len(ix.files) / 5
	if common < 3 {
		common = 3
	}
	for _, f := range ix.files {
		f.refs = 0
	}
	for _, f := range ix.files {
		for _, s := range f.Symbols {
			if len(s.Name) < 4 || df[s.Name] > common {
				continue
			}
			for _, g := range ix.files {
				if g != f && g.idents[s.Name] > 0 {
					f.refs++
				}
			}
		}
	}
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

func identCounts(data []byte) map[string]int {
	m := map[string]int{}
	for _, id := range identRe.FindAll(data, -1) {
		m[string(id)]++
	}
	return m
}

// Render returns the map within about budget tokens (≈4 chars/token).
func (ix *Index) Render(budgetTokens int) string {
	ix.Refresh()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	files := make([]*File, 0, len(ix.files))
	for _, f := range ix.files {
		if len(f.Symbols) > 0 {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].refs != files[j].refs {
			return files[i].refs > files[j].refs
		}
		return files[i].Path < files[j].Path
	})
	budget := budgetTokens * 4
	var sb strings.Builder
	sb.WriteString("REPO MAP (top-level symbols by file; use code_symbols to search, fs_read to see code):\n")
	for _, f := range files {
		block := renderFile(f)
		if sb.Len()+len(block) > budget {
			if sb.Len()+len(f.Path)+2 <= budget {
				sb.WriteString(f.Path + "\n")
			}
			continue
		}
		sb.WriteString(block)
	}
	return sb.String()
}

// maxSymbolsPerFile keeps one large file from eating the whole budget;
// types and functions are listed before methods.
const maxSymbolsPerFile = 10

func renderFile(f *File) string {
	syms := append([]Symbol(nil), f.Symbols...)
	sort.SliceStable(syms, func(i, j int) bool { return syms[i].Kind != "method" && syms[j].Kind == "method" })
	var sb strings.Builder
	sb.WriteString(f.Path + "\n")
	for i, s := range syms {
		if i == maxSymbolsPerFile {
			sb.WriteString(fmt.Sprintf("  … +%d more (code_symbols or fs_read)\n", len(syms)-i))
			break
		}
		sb.WriteString("  " + s.Signature + "\n")
	}
	return sb.String()
}

// Match is one code_symbols search hit.
type Match struct {
	Path string `json:"path"`
	Symbol
}

// Search finds symbols whose name contains query (case-insensitive).
func (ix *Index) Search(query string, limit int) []Match {
	ix.Refresh()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	q := strings.ToLower(query)
	var out []Match
	for _, f := range ix.files {
		for _, s := range f.Symbols {
			if strings.Contains(strings.ToLower(s.Name), q) {
				out = append(out, Match{Path: f.Path, Symbol: s})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ei, ej := strings.EqualFold(out[i].Name, query), strings.EqualFold(out[j].Name, query)
		if ei != ej {
			return ei
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// --- extraction -----------------------------------------------------------

func extract(rel string, data []byte) []Symbol {
	if strings.HasSuffix(rel, ".go") {
		if syms, ok := extractGo(data); ok {
			return syms
		}
	}
	return extractByPatterns(strings.ToLower(filepath.Ext(rel)), data)
}

func extractGo(data []byte) ([]Symbol, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", data, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	var syms []Symbol
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			if !x.Name.IsExported() || (x.Recv != nil && !exportedReceiver(x.Recv)) {
				continue
			}
			sig := *x
			sig.Body, sig.Doc = nil, nil
			var buf bytes.Buffer
			_ = printer.Fprint(&buf, fset, &sig)
			kind := "func"
			if x.Recv != nil {
				kind = "method"
			}
			syms = append(syms, Symbol{Name: x.Name.Name, Kind: kind, Signature: oneLine(buf.String()), Line: fset.Position(x.Pos()).Line})
		case *ast.GenDecl:
			for _, sp := range x.Specs {
				switch s := sp.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					kind := "type"
					sig := "type " + s.Name.Name
					switch t := s.Type.(type) {
					case *ast.StructType:
						sig += " struct"
					case *ast.InterfaceType:
						sig += " interface"
						kind = "interface"
					default:
						var buf bytes.Buffer
						_ = printer.Fprint(&buf, fset, t)
						sig += " " + oneLine(buf.String())
					}
					syms = append(syms, Symbol{Name: s.Name.Name, Kind: kind, Signature: sig, Line: fset.Position(s.Pos()).Line})
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							syms = append(syms, Symbol{Name: n.Name, Kind: strings.ToLower(x.Tok.String()), Signature: strings.ToLower(x.Tok.String()) + " " + n.Name, Line: fset.Position(n.Pos()).Line})
						}
					}
				}
			}
		}
	}
	return syms, true
}

// exportedReceiver reports whether a method's receiver type is exported
// (methods of unexported types are internal plumbing).
func exportedReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	t := recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	id, ok := t.(*ast.Ident)
	return ok && id.IsExported()
}

type linePattern struct {
	kind string
	re   *regexp.Regexp
}

var langPatterns = map[string][]linePattern{
	".py": {
		{"class", regexp.MustCompile(`^class\s+(\w+)`)},
		{"func", regexp.MustCompile(`^(?:async\s+)?def\s+(\w+)`)},
	},
	".js":  jsPatterns,
	".jsx": jsPatterns,
	".ts":  jsPatterns,
	".tsx": jsPatterns,
	".rs": {
		{"func", regexp.MustCompile(`^pub(?:\([^)]*\))?\s+(?:async\s+)?fn\s+(\w+)`)},
		{"type", regexp.MustCompile(`^pub(?:\([^)]*\))?\s+(?:struct|enum|trait)\s+(\w+)`)},
	},
	".java": classMethodPatterns,
	".cs":   classMethodPatterns,
	".kt":   {{"class", regexp.MustCompile(`^(?:\w+\s+)*class\s+(\w+)`)}, {"func", regexp.MustCompile(`^(?:\w+\s+)*fun\s+(\w+)`)}},
	".rb":   {{"class", regexp.MustCompile(`^(?:class|module)\s+(\w+)`)}, {"func", regexp.MustCompile(`^\s*def\s+(\w+)`)}},
	".php":  {{"class", regexp.MustCompile(`^(?:\w+\s+)*class\s+(\w+)`)}, {"func", regexp.MustCompile(`^\s*(?:public\s+|static\s+)*function\s+(\w+)`)}},
}

var jsPatterns = []linePattern{
	{"class", regexp.MustCompile(`^export\s+(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`)},
	{"func", regexp.MustCompile(`^export\s+(?:default\s+)?(?:async\s+)?function\s*\*?\s*(\w+)`)},
	{"const", regexp.MustCompile(`^export\s+(?:const|let)\s+(\w+)`)},
	{"type", regexp.MustCompile(`^export\s+(?:interface|type|enum)\s+(\w+)`)},
}

var classMethodPatterns = []linePattern{
	{"class", regexp.MustCompile(`^\s*public\s+(?:\w+\s+)*(?:class|interface|record|enum)\s+(\w+)`)},
	{"method", regexp.MustCompile(`^\s+public\s+(?:static\s+|async\s+|override\s+|virtual\s+|final\s+)*[\w<>\[\],.? ]+\s+(\w+)\s*\(`)},
}

func extractByPatterns(ext string, data []byte) []Symbol {
	pats := langPatterns[ext]
	if len(pats) == 0 {
		return nil
	}
	var syms []Symbol
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		for _, p := range pats {
			if m := p.re.FindStringSubmatch(text); m != nil {
				sig := strings.TrimSpace(text)
				sig = strings.TrimSuffix(strings.TrimSuffix(sig, "{"), ":")
				syms = append(syms, Symbol{Name: m[1], Kind: p.kind, Signature: oneLine(strings.TrimSpace(sig)), Line: line})
				break
			}
		}
	}
	return syms
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
