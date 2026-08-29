package anchor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAnchorStoreSQLDirect(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	err = CreateAnchorTable(context.Background(), db)
	if err != nil {
		t.Fatalf("CreateAnchorTable: %v", err)
	}

	as := NewAnchorStoreSQL(db)

	anchor := Anchor{
		SessionID: "test-session-1",
		Content:   "Decisión: usar SQLite para embeddings",
		Source:    "user",
		Tags:      []string{"arch", "db"},
	}
	created, err := as.Create(context.Background(), anchor)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	got, err := as.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Content != anchor.Content {
		t.Errorf("content mismatch: %s vs %s", got.Content, anchor.Content)
	}

	list, err := as.List(context.Background(), "test-session-1")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 anchor, got %d", len(list))
	}

	created.Content = "Decisión ACTUALIZADA: usar SQLite para embeddings"
	if err := as.Update(context.Background(), created); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	got, _ = as.Get(context.Background(), created.ID)
	if got.Content != created.Content {
		t.Errorf("update not persisted: %s", got.Content)
	}

	if err := as.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	list, _ = as.List(context.Background(), "test-session-1")
	if len(list) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(list))
	}
}

func TestAnchorStoreMultipleSessions(t *testing.T) {
	db, _ := sql.Open("sqlite", "file::memory:?cache=shared")
	defer db.Close()
	_ = CreateAnchorTable(context.Background(), db)
	as := NewAnchorStoreSQL(db)

	as.Create(context.Background(), Anchor{SessionID: "s1", Content: "anchor in s1"})
	as.Create(context.Background(), Anchor{SessionID: "s1", Content: "another in s1"})
	as.Create(context.Background(), Anchor{SessionID: "s2", Content: "anchor in s2"})

	list1, _ := as.List(context.Background(), "s1")
	if len(list1) != 2 {
		t.Errorf("s1 should have 2 anchors, got %d", len(list1))
	}

	list2, _ := as.List(context.Background(), "s2")
	if len(list2) != 1 {
		t.Errorf("s2 should have 1 anchor, got %d", len(list2))
	}

	all, _ := as.ListAll(context.Background())
	if len(all) != 3 {
		t.Errorf("expected 3 total anchors, got %d", len(all))
	}
}

func TestAnchorTags(t *testing.T) {
	db, _ := sql.Open("sqlite", "file::memory:?cache=shared")
	defer db.Close()
	_ = CreateAnchorTable(context.Background(), db)
	as := NewAnchorStoreSQL(db)

	created, _ := as.Create(context.Background(), Anchor{
		SessionID: "sess",
		Content:   "tagged anchor",
		Tags:      []string{"tag1", "tag2"},
	})

	got, _ := as.Get(context.Background(), created.ID)
	if len(got.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(got.Tags))
	}
}

func TestAnchorCreatedAt(t *testing.T) {
	db, _ := sql.Open("sqlite", "file::memory:?cache=shared")
	defer db.Close()
	_ = CreateAnchorTable(context.Background(), db)
	as := NewAnchorStoreSQL(db)

	before := time.Now()
	created, _ := as.Create(context.Background(), Anchor{SessionID: "s", Content: "test"})
	after := time.Now()

	if created.CreatedAt.Before(before.Add(-time.Second)) || created.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("CreatedAt not in expected range: %v", created.CreatedAt)
	}
}
