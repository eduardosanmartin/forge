package anchor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"

	"github.com/eduardosanmartin/forge/internal/store"
)

// Anchor represents a persistent fact/decision anchored in a session.
type Anchor struct {
	ID        int64
	SessionID string
	Content   string
	Source    string // "user" | "assistant" | "auto"
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AnchorStore manages persistent anchors using the forge store.
type AnchorStore struct {
	db *sql.DB
}

// NewAnchorStore creates a new AnchorStore using the given forge store.
func NewAnchorStore(s *store.Store) *AnchorStore {
	// Access the underlying DB from store
	// We need to get the DB - store.Store has a private db field
	// For now, create a new connection to the same DB
	// In practice, we'd add a method to store.Store to expose DB or run queries
	// Here we'll create the anchor tables if not exist
	return &AnchorStore{}
}

// CreateAnchorTable creates the anchors table if not exists.
// This should be called during store initialization/migration.
func CreateAnchorTable(ctx context.Context, db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS anchors (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		content     TEXT NOT NULL,
		source      TEXT NOT NULL DEFAULT 'user',
		tags        TEXT NOT NULL DEFAULT '[]', -- JSON array
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_anchors_session ON anchors(session_id);
	CREATE INDEX IF NOT EXISTS idx_anchors_created ON anchors(created_at);
	`
	_, err := db.ExecContext(ctx, schema)
	return err
}

// AnchorStoreSQL provides direct SQL access for anchors.
// This is a wrapper around the forge store's DB.
type AnchorStoreSQL struct {
	db *sql.DB
}

// NewAnchorStoreSQL creates an anchor store with direct SQL access.
func NewAnchorStoreSQL(db *sql.DB) *AnchorStoreSQL {
	return &AnchorStoreSQL{db: db}
}

// Create inserts a new anchor.
func (a *AnchorStoreSQL) Create(ctx context.Context, anchor Anchor) (Anchor, error) {
	if anchor.SessionID == "" {
		return Anchor{}, errors.New("session_id required")
	}
	if anchor.Content == "" {
		return Anchor{}, errors.New("content required")
	}
	if anchor.Source == "" {
		anchor.Source = "user"
	}
	now := time.Now().UnixMilli()

	tagsJSON, _ := json.Marshal(anchor.Tags)

	res, err := a.db.ExecContext(ctx, `
		INSERT INTO anchors (session_id, content, source, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, anchor.SessionID, anchor.Content, anchor.Source, string(tagsJSON), now, now)
	if err != nil {
		return Anchor{}, err
	}

	id, _ := res.LastInsertId()
	anchor.ID = id
	anchor.CreatedAt = time.UnixMilli(now)
	anchor.UpdatedAt = time.UnixMilli(now)
	return anchor, nil
}

// Get retrieves an anchor by ID.
func (a *AnchorStoreSQL) Get(ctx context.Context, id int64) (Anchor, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT id, session_id, content, source, tags, created_at, updated_at
		FROM anchors WHERE id = ?
	`, id)

	var anchor Anchor
	var tagsJSON string
	var createdMs, updatedMs int64
	err := row.Scan(&anchor.ID, &anchor.SessionID, &anchor.Content, &anchor.Source, &tagsJSON, &createdMs, &updatedMs)
	if err != nil {
		return Anchor{}, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &anchor.Tags)
	anchor.CreatedAt = time.UnixMilli(createdMs)
	anchor.UpdatedAt = time.UnixMilli(updatedMs)
	return anchor, nil
}

// List returns all anchors for a session.
func (a *AnchorStoreSQL) List(ctx context.Context, sessionID string) ([]Anchor, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, session_id, content, source, tags, created_at, updated_at
		FROM anchors WHERE session_id = ? ORDER BY created_at ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return a.scanAnchors(rows)
}

// ListAll returns all anchors across all sessions.
func (a *AnchorStoreSQL) ListAll(ctx context.Context) ([]Anchor, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, session_id, content, source, tags, created_at, updated_at
		FROM anchors ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return a.scanAnchors(rows)
}

// Update updates an anchor's content, source, or tags.
func (a *AnchorStoreSQL) Update(ctx context.Context, anchor Anchor) error {
	if anchor.ID == 0 {
		return errors.New("anchor ID required")
	}
	now := time.Now().UnixMilli()
	tagsJSON, _ := json.Marshal(anchor.Tags)

	res, err := a.db.ExecContext(ctx, `
		UPDATE anchors SET content = ?, source = ?, tags = ?, updated_at = ? WHERE id = ?
	`, anchor.Content, anchor.Source, string(tagsJSON), now, anchor.ID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return errors.New("anchor not found")
	}
	return nil
}

// Delete removes an anchor by ID.
func (a *AnchorStoreSQL) Delete(ctx context.Context, id int64) error {
	res, err := a.db.ExecContext(ctx, `DELETE FROM anchors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return errors.New("anchor not found")
	}
	return nil
}

// scanAnchors scans rows into Anchor slice.
func (a *AnchorStoreSQL) scanAnchors(rows *sql.Rows) ([]Anchor, error) {
	var anchors []Anchor
	for rows.Next() {
		var anchor Anchor
		var tagsJSON string
		var createdMs, updatedMs int64
		if err := rows.Scan(&anchor.ID, &anchor.SessionID, &anchor.Content, &anchor.Source, &tagsJSON, &createdMs, &updatedMs); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &anchor.Tags)
		anchor.CreatedAt = time.UnixMilli(createdMs)
		anchor.UpdatedAt = time.UnixMilli(updatedMs)
		anchors = append(anchors, anchor)
	}
	return anchors, rows.Err()
}
