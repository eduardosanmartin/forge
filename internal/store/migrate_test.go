// Package store implements SQLite-backed session and message persistence for forge.
package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrationV0ToV1(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migrate.db")

	// Create database with v0 schema (no metadata column in sessions)
	{
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
		if err != nil {
			t.Fatalf("sql.Open failed: %v", err)
		}
		defer db.Close()

		// Create v0 schema (without metadata column)
		_, err = db.ExecContext(context.Background(), `
			CREATE TABLE sessions (
				id TEXT PRIMARY KEY,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			);
			CREATE TABLE messages (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				role TEXT NOT NULL,
				content TEXT NOT NULL,
				tool_calls TEXT,
				tool_call_id TEXT,
				name TEXT,
				usage TEXT,
				created_at INTEGER NOT NULL,
				UNIQUE(session_id, seq)
			);
			CREATE INDEX idx_messages_session_seq ON messages(session_id, seq);
			CREATE TABLE config_snapshots (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				config_json TEXT NOT NULL,
				created_at INTEGER NOT NULL
			);
			CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
			INSERT INTO schema_version (version) VALUES (0);
		`)
		if err != nil {
			t.Fatalf("create v0 schema failed: %v", err)
		}
	}

	// Now open with store - should run migration to v1
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open with migration failed: %v", err)
	}
	defer s.Close()

	// Verify schema_version is now 1
	var version int
	err = s.db.QueryRowContext(context.Background(), `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version failed: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d, want 1 after migration", version)
	}

	// Verify sessions table has metadata column
	rows, err := s.db.QueryContext(context.Background(), `PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()

	hasMetadata := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info failed: %v", err)
		}
		if name == "metadata" {
			hasMetadata = true
			if ctype != "TEXT" {
				t.Fatalf("metadata column type = %q, want TEXT", ctype)
			}
			if notnull != 1 {
				t.Fatalf("metadata column notnull = %d, want 1", notnull)
			}
			if dfltValue.String != "{}" && dfltValue.String != "'{}'" {
				t.Fatalf("metadata default = %q, want {}", dfltValue.String)
			}
		}
	}
	if !hasMetadata {
		t.Fatal("sessions table missing metadata column after migration")
	}

	// Verify we can create and query sessions with metadata
	ctx := context.Background()
	session, err := s.CreateSession(ctx, map[string]any{"model": "test"})
	if err != nil {
		t.Fatalf("CreateSession after migration failed: %v", err)
	}

	got, err := s.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession after migration failed: %v", err)
	}
	if got.Metadata["model"] != "test" {
		t.Fatalf("metadata not working after migration: %v", got.Metadata)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migrate2.db")

	// Open once (runs migration)
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 1 failed: %v", err)
	}
	s1.Close()

	// Open again - should not re-run migration
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 2 failed: %v", err)
	}
	defer s2.Close()

	var version int
	err = s2.db.QueryRowContext(context.Background(), `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version failed: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d, want 1", version)
	}
}

func TestMigrationStatus(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "status.db")

	// Fresh database
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	applied, pending, err := MigrationStatus(context.Background(), s.db)
	if err != nil {
		t.Fatalf("MigrationStatus failed: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if pending {
		t.Fatal("pending = true, want false for current version")
	}
}

func TestSetSchemaVersionForTesting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "version.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Set version to 0
	err = SetSchemaVersionForTesting(context.Background(), s.db, 0)
	if err != nil {
		t.Fatalf("SetSchemaVersionForTesting failed: %v", err)
	}

	var version int
	err = s.db.QueryRowContext(context.Background(), `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version failed: %v", err)
	}
	if version != 0 {
		t.Fatalf("version = %d, want 0", version)
	}

	// Re-open should run migration again
	s.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Re-open after version reset failed: %v", err)
	}
	defer s2.Close()

	err = s2.db.QueryRowContext(context.Background(), `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema_version after re-open failed: %v", err)
	}
	if version != 1 {
		t.Fatalf("version after re-open = %d, want 1", version)
	}
}

func TestMigrationVersion(t *testing.T) {
	if MigrationVersion() != 1 {
		t.Fatalf("MigrationVersion() = %d, want 1", MigrationVersion())
	}
}
