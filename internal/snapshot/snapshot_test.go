package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func TestSnapshotAndRestore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ws, base := t.TempDir(), t.TempDir()
	write(t, ws, "keep.txt", "v1")
	write(t, ws, "edit.txt", "original")
	write(t, ws, "gone.txt", "will be deleted")
	write(t, ws, ".gitignore", "ignored.log\n")
	write(t, ws, "ignored.log", "noise")
	write(t, ws, ".forge/state.json", "{}")
	// A project repo of its own must be left alone.
	write(t, ws, ".git/HEAD", "ref: refs/heads/main\n")

	s, err := Open(base, ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snap, err := s.Snapshot(ctx, "sess", "turn-1", "before turn 1")
	if err != nil || snap == "" {
		t.Fatalf("Snapshot: %q %v", snap, err)
	}
	if again, _ := s.Snapshot(ctx, "sess", "turn-1", "second call"); again != "" {
		t.Fatal("a second snapshot in the same turn must be a no-op")
	}

	// The turn's changes.
	write(t, ws, "edit.txt", "changed by the agent")
	os.Remove(filepath.Join(ws, "gone.txt"))
	write(t, ws, "new/created.go", "package x")
	write(t, ws, "ignored.log", "more noise")

	safety, err := s.Restore(ctx, snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if v, _ := read(t, ws, "edit.txt"); v != "original" {
		t.Errorf("edit.txt = %q, want restored", v)
	}
	if v, ok := read(t, ws, "gone.txt"); !ok || v != "will be deleted" {
		t.Error("gone.txt must be restored")
	}
	if _, ok := read(t, ws, "new/created.go"); ok {
		t.Error("files created after the snapshot must be removed")
	}
	if v, _ := read(t, ws, "ignored.log"); v != "more noise" {
		t.Error("ignored files must be left alone")
	}
	if v, _ := read(t, ws, ".git/HEAD"); v != "ref: refs/heads/main\n" {
		t.Error("the project's own .git must be untouched")
	}
	if _, ok := read(t, ws, ".forge/state.json"); !ok {
		t.Error(".forge state must be untouched")
	}

	// The undo is itself undoable.
	if _, err := s.Restore(ctx, safety); err != nil {
		t.Fatalf("redo: %v", err)
	}
	if v, _ := read(t, ws, "edit.txt"); v != "changed by the agent" {
		t.Errorf("after redo edit.txt = %q", v)
	}
	if _, ok := read(t, ws, "new/created.go"); !ok {
		t.Error("after redo the created file must be back")
	}

	list, err := s.List()
	if err != nil || len(list) < 3 || list[len(list)-1].Commit != snap {
		t.Fatalf("List = %+v, %v", list, err)
	}
}
