-- Schema for deepseek-lens. Applied with CREATE TABLE IF NOT EXISTS only —
-- no migration framework in v1 (see br-GI-1-06). All columns any of the 13
-- beads will eventually need are created here, even ones only a later bead
-- populates: session_id, cost_usd, cost_source, stop_reason, replay_of,
-- replay_edits, prefix_hash.
--
-- Times are stored as Unix nanoseconds (INTEGER) so a Go time.Time round
-- trips exactly through UnixNano()/time.Unix(0, ns).

CREATE TABLE IF NOT EXISTS requests (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at             INTEGER NOT NULL,
    ttfb_ns                INTEGER NOT NULL,
    duration_ns            INTEGER NOT NULL,
    method                 TEXT NOT NULL,
    path                   TEXT NOT NULL,
    remote_addr            TEXT NOT NULL,
    status                 INTEGER NOT NULL,
    req_headers            TEXT NOT NULL,
    resp_headers           TEXT NOT NULL,
    req_body               BLOB,
    resp_body              BLOB,
    input_tokens           INTEGER NOT NULL DEFAULT 0,
    output_tokens          INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens      INTEGER NOT NULL DEFAULT 0,
    stop_reason            TEXT,
    model_requested        TEXT NOT NULL DEFAULT '',
    model_resolved         TEXT NOT NULL DEFAULT '',
    error_text             TEXT,
    session_header         TEXT,
    session_id             TEXT,
    cost_usd               REAL,
    cost_source            TEXT,
    replay_of              INTEGER,
    replay_edits           TEXT,
    prefix_hash            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at);
CREATE INDEX IF NOT EXISTS idx_requests_session_id ON requests(session_id);

CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS warnings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    severity   TEXT NOT NULL,
    detail     TEXT NOT NULL,
    path       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_warnings_request_id ON warnings(request_id);
