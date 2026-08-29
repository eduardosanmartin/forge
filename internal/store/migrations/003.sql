-- Migration v3: Add config_snapshots table for debugging/replay
-- Version: 3

CREATE TABLE IF NOT EXISTS config_snapshots (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    config_json TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);