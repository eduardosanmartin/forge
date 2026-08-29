-- Migration v2: Add anchors table for persistent anchored facts/decisions
-- Version: 2

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