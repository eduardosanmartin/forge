// Package store implements SQLite-backed session and message persistence for forge.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const currentSchemaVersion = 1

// runMigrations executes all pending migrations in order.
func runMigrations(ctx context.Context, db *sql.DB) error {
	// Ensure schema_version table exists
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	// Get current version, insert initial row if empty
	var version int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Table exists but empty, insert version 0
			if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
				return fmt.Errorf("insert initial schema version: %w", err)
			}
			version = 0
		} else {
			return fmt.Errorf("read schema version: %w", err)
		}
	}

	if version >= currentSchemaVersion {
		return nil // Already up to date
	}

	// Run migrations from version+1 to currentSchemaVersion
	for v := version + 1; v <= currentSchemaVersion; v++ {
		if err := runMigration(ctx, db, v); err != nil {
			return fmt.Errorf("migration v%d: %w", v, err)
		}
	}

	return nil
}

func runMigration(ctx context.Context, db *sql.DB, version int) error {
	// Execute migration in a transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	switch version {
	case 1:
		// Run embedded SQL for base schema
		filename := fmt.Sprintf("migrations/%03d.sql", version)
		sqlBytes, err := migrationFiles.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filename, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}

		// Migration v1: ensure sessions table has metadata column (for v0->v1 upgrades)
		// Check if metadata column exists
		var count int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'metadata'`).Scan(&count)
		if err != nil {
			return fmt.Errorf("check metadata column: %w", err)
		}
		if count == 0 {
			// Column doesn't exist, add it
			if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`); err != nil {
				return fmt.Errorf("add metadata column: %w", err)
			}
		}

	default:
		filename := fmt.Sprintf("migrations/%03d.sql", version)
		sqlBytes, err := migrationFiles.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filename, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}
	}

	// Update schema version within the same transaction
	// Use DELETE + INSERT to avoid any PRIMARY KEY update issues
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("clear schema version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("insert schema version %d: %w", version, err)
	}

	// Verify the version was actually updated
	var verifyVersion int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&verifyVersion); err != nil {
		return fmt.Errorf("verify schema version: %w", err)
	}
	if verifyVersion != version {
		return fmt.Errorf("schema version mismatch: got %d, want %d", verifyVersion, version)
	}

	return tx.Commit()
}

// MigrationVersion returns the current schema version.
func MigrationVersion() int {
	return currentSchemaVersion
}

// MigrationStatus returns the current applied version and whether migrations are pending.
func MigrationStatus(ctx context.Context, db *sql.DB) (applied int, pending bool, err error) {
	var version sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("read schema version: %w", err)
	}
	applied = int(version.Int64)
	pending = applied < currentSchemaVersion
	return applied, pending, nil
}

// initSchemaVersion initializes the schema_version table with version 0 if empty.
func initSchemaVersion(ctx context.Context, db *sql.DB) error {
	// Ensure table exists first
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check schema_version count: %w", err)
	}
	if count == 0 {
		_, err = db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`)
		if err != nil {
			return fmt.Errorf("insert initial schema version: %w", err)
		}
	}
	return nil
}

// SetSchemaVersionForTesting sets the schema version directly (testing only).
func SetSchemaVersionForTesting(ctx context.Context, db *sql.DB, version int) error {
	_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO schema_version (version) VALUES (?)`, version)
	return err
}

// ParseVersion parses a version string to int.
func ParseVersion(s string) (int, error) {
	return strconv.Atoi(s)
}
