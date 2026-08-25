-- Initial schema for forge session store
-- Version: 1

-- Sessions: one row per conversation session
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,          -- UUID v4
    created_at  INTEGER NOT NULL,          -- unix ms
    updated_at  INTEGER NOT NULL,          -- unix ms
    metadata    TEXT NOT NULL DEFAULT '{}' -- JSON: model, provider, config snapshot, etc.
);

-- Messages: append-only log per session (turn = user + assistant + tool calls)
CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,      -- monotonically increasing per session
    role            TEXT NOT NULL,         -- "user" | "assistant" | "tool"
    content         TEXT NOT NULL,         -- raw content (fenced tool results already wrapped by tools layer)
    tool_calls      TEXT,                  -- JSON array of ToolCall (assistant only), NULL otherwise
    tool_call_id    TEXT,                  -- for tool results
    name            TEXT,                  -- tool name for tool messages
    usage           TEXT,                  -- JSON Usage (assistant only), NULL otherwise
    created_at      INTEGER NOT NULL,      -- unix ms
    UNIQUE(session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);

-- Config snapshots (optional, for debugging/replay)
CREATE TABLE IF NOT EXISTS config_snapshots (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    config_json TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);