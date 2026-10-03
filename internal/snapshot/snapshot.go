// Package snapshot keeps per-turn snapshots of a workspace in a "shadow"
// git repository outside it (~/.forge/snapshots/<workspace hash>), so the
// agent's file changes can be undone (forge undo) without touching the
// project's own repository, index, branches or history.
//
// Only the workspace's files are covered: side effects of shell commands
// outside it (installed packages, databases, network calls) are not.
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one recorded snapshot.
type Entry struct {
	Commit    string    `json:"commit"`
	SessionID string    `json:"session_id,omitempty"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the shadow repository of one workspace.
type Store struct {
	gitDir   string
	workTree string

	mu       sync.Mutex
	inited   bool
	lastTurn map[string]bool // turn IDs already snapshotted (bounded)
	order    []string
}

// Open returns the Store for workspaceRoot under baseDir (typically
// ~/.forge/snapshots). The shadow repo is created lazily on first use.
func Open(baseDir, workspaceRoot string) (*Store, error) {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	return &Store{
		gitDir:   filepath.Join(baseDir, hex.EncodeToString(sum[:8])),
		workTree: abs,
		lastTurn: make(map[string]bool),
	}, nil
}

// GitDir is the shadow repository's location.
func (s *Store) GitDir() string { return s.gitDir }

func (s *Store) git(ctx context.Context, args ...string) (string, error) {
	full := append([]string{
		"--git-dir=" + s.gitDir, "--work-tree=" + s.workTree,
		"-c", "user.name=forge-snapshot", "-c", "user.email=snapshot@forge.local",
		"-c", "core.autocrlf=false", "-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = s.workTree
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// ensure initializes the shadow repo once. The project's own .git and
// forge's runtime state are excluded; the project's .gitignore files still
// apply (git reads them from the work tree).
func (s *Store) ensure(ctx context.Context) error {
	if s.inited {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.gitDir, "HEAD")); err != nil {
		if err := os.MkdirAll(s.gitDir, 0o700); err != nil {
			return err
		}
		if _, err := s.git(ctx, "init", "--quiet"); err != nil {
			return err
		}
	}
	exclude := filepath.Join(s.gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(exclude, []byte(".git/\n.forge/\n"), 0o600); err != nil {
		return err
	}
	s.inited = true
	return nil
}

// Snapshot records the workspace's current state. turnID, when set,
// deduplicates: only the first call per turn snapshots (the state BEFORE
// the turn's first change), later calls in the same turn are no-ops.
func (s *Store) Snapshot(ctx context.Context, sessionID, turnID, label string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnID != "" {
		if s.lastTurn[turnID] {
			return "", nil
		}
		s.lastTurn[turnID] = true
		s.order = append(s.order, turnID)
		if len(s.order) > 256 {
			delete(s.lastTurn, s.order[0])
			s.order = s.order[1:]
		}
	}
	return s.snapshotLocked(ctx, sessionID, label)
}

func (s *Store) snapshotLocked(ctx context.Context, sessionID, label string) (string, error) {
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	if _, err := s.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := s.git(ctx, "commit", "--quiet", "--allow-empty", "-m", label); err != nil {
		return "", err
	}
	out, err := s.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	e := Entry{Commit: strings.TrimSpace(out), SessionID: sessionID, Label: label, CreatedAt: time.Now().UTC()}
	if err := s.appendLog(e); err != nil {
		return "", err
	}
	return e.Commit, nil
}

func (s *Store) appendLog(e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.gitDir, "forge-snapshots.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// List returns recorded snapshots, newest first.
func (s *Store) List() ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(s.gitDir, "forge-snapshots.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e Entry
		if json.Unmarshal([]byte(line), &e) == nil && e.Commit != "" {
			out = append(out, e)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Restore returns the workspace's files to snapshot commit: files changed
// or deleted since are restored, files created since are removed. The
// current state is snapshotted first ("before undo"), so an undo can
// itself be undone. Returns that safety snapshot's commit.
func (s *Store) Restore(ctx context.Context, commit string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(ctx); err != nil {
		return "", err
	}
	if _, err := s.git(ctx, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return "", fmt.Errorf("unknown snapshot %q", commit)
	}
	safety, err := s.snapshotLocked(ctx, "", "before undo to "+shortSHA(commit))
	if err != nil {
		return "", err
	}
	added, err := s.git(ctx, "diff", "--name-only", "--diff-filter=A", "-z", commit, safety)
	if err != nil {
		return "", err
	}
	for _, rel := range strings.Split(added, "\x00") {
		if rel == "" {
			continue
		}
		p := filepath.Join(s.workTree, filepath.FromSlash(rel))
		if !strings.HasPrefix(p, s.workTree+string(os.PathSeparator)) {
			continue // defense in depth: never delete outside the workspace
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if _, err := s.git(ctx, "checkout", commit, "--", "."); err != nil {
		return "", err
	}
	return safety, nil
}

func shortSHA(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	return c
}
