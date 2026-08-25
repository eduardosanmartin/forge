// Package store implements SQLite-backed session and message persistence for forge.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/eduardosanmartin/forge/internal/llm"
)

// Open creates a new Store at the given path.
// Path supports "~/" expansion. Creates parent directories, enables WAL mode,
// runs migrations, and configures the connection pool.
func Open(path string) (*Store, error) {
	expanded, err := expandPath(path)
	if err != nil {
		return nil, fmt.Errorf("expand path: %w", err)
	}

	dir := filepath.Dir(expanded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create directory %s: %w", dir, err)
	}

	// Use modernc.org/sqlite DSN format
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", expanded)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite serializes writes anyway; single connection avoids busy lock contention
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run migrations (creates schema_version table and applies migrations)
	if err := runMigrations(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	s := &Store{db: db}
	return s, nil
}

// Store holds the database connection and provides session/message operations.
type Store struct {
	db *sql.DB
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// expandPath expands a leading "~/" in path.
func expandPath(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if len(path) >= 2 && (path[:2] == "~/" || path[:2] == `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		rest := path[2:]
		return filepath.Join(home, filepath.FromSlash(rest)), nil
	}
	return path, nil
}

// nowMs returns current time in milliseconds since epoch.
func nowMs() int64 {
	return time.Now().UnixMilli()
}

// uuid generates a UUID v4 string using crypto/rand.
func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based if rand fails (should not happen)
		for i := range b {
			b[i] = byte(time.Now().UnixNano() + int64(i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Sessions

// CreateSession creates a new session with the given metadata.
func (s *Store) CreateSession(ctx context.Context, metadata map[string]any) (Session, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Session{}, fmt.Errorf("marshal metadata: %w", err)
	}

	id := uuid()
	now := nowMs()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at, metadata) VALUES (?, ?, ?, ?)`,
		id, now, now, string(metadataJSON))
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit session: %w", err)
	}

	return Session{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  metadata,
	}, nil
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var session Session
	var metadataJSON string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, created_at, updated_at, metadata FROM sessions WHERE id = ?`,
		id).Scan(&session.ID, &session.CreatedAt, &session.UpdatedAt, &metadataJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, fmt.Errorf("query session: %w", err)
	}

	if metadataJSON != "" && metadataJSON != "{}" {
		if err := json.Unmarshal([]byte(metadataJSON), &session.Metadata); err != nil {
			return Session{}, fmt.Errorf("unmarshal metadata: %w", err)
		}
	} else {
		session.Metadata = map[string]any{}
	}

	return session, nil
}

// UpdateSessionMetadata updates a session's metadata (merges with existing).
func (s *Store) UpdateSessionMetadata(ctx context.Context, id string, metadata map[string]any) error {
	// Get existing metadata
	existing, err := s.GetSession(ctx, id)
	if err != nil {
		return err
	}

	// Merge metadata
	for k, v := range metadata {
		existing.Metadata[k] = v
	}

	metadataJSON, err := json.Marshal(existing.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	now := nowMs()
	_, err = s.db.ExecContext(ctx,
		`UPDATE sessions SET metadata = ?, updated_at = ? WHERE id = ?`,
		string(metadataJSON), now, id)
	if err != nil {
		return fmt.Errorf("update session metadata: %w", err)
	}

	return nil
}

// ListSessions returns sessions ordered by updated_at DESC (newest first).
func (s *Store) ListSessions(ctx context.Context, limit, offset int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, updated_at, metadata FROM sessions ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var metadataJSON string
		if err := rows.Scan(&session.ID, &session.CreatedAt, &session.UpdatedAt, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if metadataJSON != "" && metadataJSON != "{}" {
			if err := json.Unmarshal([]byte(metadataJSON), &session.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal metadata: %w", err)
			}
		} else {
			session.Metadata = map[string]any{}
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	return sessions, nil
}

// DeleteSession deletes a session and all its messages (cascade).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// Messages

// AppendMessage appends a message to a session. Seq is auto-assigned.
// Returns the assigned seq and ID.
func (s *Store) AppendMessage(ctx context.Context, msg *Message) (int, int64, error) {
	if msg.SessionID == "" {
		return 0, 0, ErrEmptySessionID
	}

	// Serialize tool_calls and usage
	var toolCallsJSON, usageJSON string
	if len(msg.ToolCalls) > 0 {
		b, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return 0, 0, fmt.Errorf("marshal tool_calls: %w", err)
		}
		toolCallsJSON = string(b)
	}
	if msg.Usage != nil {
		b, err := json.Marshal(msg.Usage)
		if err != nil {
			return 0, 0, fmt.Errorf("marshal usage: %w", err)
		}
		usageJSON = string(b)
	}

	now := nowMs()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Determine next seq number atomically within transaction
	var seq int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), -1) + 1 FROM messages WHERE session_id = ?`,
		msg.SessionID).Scan(&seq)
	if err != nil {
		return 0, 0, fmt.Errorf("get next seq: %w", err)
	}

	// Insert message
	result, err := tx.ExecContext(ctx,
		`INSERT INTO messages (session_id, seq, role, content, tool_calls, tool_call_id, name, usage, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.SessionID, seq, msg.Role, msg.Content,
		nullString(toolCallsJSON), nullString(msg.ToolCallID), nullString(msg.Name),
		nullString(usageJSON), now)
	if err != nil {
		return 0, 0, fmt.Errorf("insert message: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, 0, fmt.Errorf("last insert id: %w", err)
	}

	// Update session updated_at
	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		now, msg.SessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("update session timestamp: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit message: %w", err)
	}

	msg.ID = id
	msg.Seq = seq
	msg.CreatedAt = now
	return seq, id, nil
}

// GetMessages returns messages for a session, newest first.
func (s *Store) GetMessages(ctx context.Context, sessionID string, limit, offset int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, seq, role, content, tool_calls, tool_call_id, name, usage, created_at
		 FROM messages WHERE session_id = ? ORDER BY seq DESC LIMIT ? OFFSET ?`,
		sessionID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	return messages, nil
}

// GetMessagesSince returns messages for a session with seq > sinceSeq, oldest first.
func (s *Store) GetMessagesSince(ctx context.Context, sessionID string, sinceSeq int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, seq, role, content, tool_calls, tool_call_id, name, usage, created_at
		 FROM messages WHERE session_id = ? AND seq > ? ORDER BY seq ASC`,
		sessionID, sinceSeq)
	if err != nil {
		return nil, fmt.Errorf("query messages since: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages since: %w", err)
	}

	return messages, nil
}

// scanMessage scans a message from a row scanner.
func scanMessage(scanner interface {
	Scan(dest ...any) error
}) (Message, error) {
	var msg Message
	var toolCallsJSON, toolCallID, name, usageJSON sql.NullString

	err := scanner.Scan(
		&msg.ID, &msg.SessionID, &msg.Seq, &msg.Role, &msg.Content,
		&toolCallsJSON, &toolCallID, &name, &usageJSON, &msg.CreatedAt)
	if err != nil {
		return Message{}, fmt.Errorf("scan message: %w", err)
	}

	if toolCallsJSON.Valid && toolCallsJSON.String != "" {
		if err := json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls); err != nil {
			return Message{}, fmt.Errorf("unmarshal tool_calls: %w", err)
		}
	}
	if toolCallID.Valid {
		msg.ToolCallID = toolCallID.String
	}
	if name.Valid {
		msg.Name = name.String
	}
	if usageJSON.Valid && usageJSON.String != "" {
		var usage llm.Usage
		if err := json.Unmarshal([]byte(usageJSON.String), &usage); err != nil {
			return Message{}, fmt.Errorf("unmarshal usage: %w", err)
		}
		msg.Usage = &usage
	}

	return msg, nil
}

// nullString returns a sql.NullString from a string (NULL if empty).
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// Maintenance

// Vacuum runs SQLite VACUUM to reclaim space.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

// Stats returns database statistics.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats

	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&stats.SessionCount)
	if err != nil {
		return Stats{}, fmt.Errorf("count sessions: %w", err)
	}

	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&stats.MessageCount)
	if err != nil {
		return Stats{}, fmt.Errorf("count messages: %w", err)
	}

	// Get database file size
	var pageCount, pageSize int64
	err = s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount)
	if err != nil {
		return Stats{}, fmt.Errorf("page_count: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
	if err != nil {
		return Stats{}, fmt.Errorf("page_size: %w", err)
	}
	stats.DBSizeBytes = pageCount * pageSize

	return stats, nil
}

// Error sentinels
var (
	ErrSessionNotFound = errors.New("session not found")
	ErrEmptySessionID  = errors.New("session_id must not be empty")
)
