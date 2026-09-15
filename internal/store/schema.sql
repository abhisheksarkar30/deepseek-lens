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
    prefix_hash            TEXT NOT NULL DEFAULT '',
    -- SET NULL, not the default RESTRICT: PurgeOlderThan must still be able
    -- to delete an old original that a newer, kept replay row points back
    -- to (fail open) — losing that linkage is an acceptable degradation,
    -- failing the purge is not.
    FOREIGN KEY (replay_of) REFERENCES requests(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at);
CREATE INDEX IF NOT EXISTS idx_requests_session_id ON requests(session_id);

-- prefix_hash is the session's correlation key, and its NULL-ness is
-- load-bearing (br-GI-1-12): NULL means the session was keyed by an explicit
-- x-lens-session header, '' means it is the null-prefix bucket for calls
-- whose body did not parse, and anything else is the parse.PrefixHash it was
-- resolved from. `prefix_hash = ?` therefore never matches a header-keyed
-- session, which is what keeps "header on one call, absent on the other"
-- from grouping.
CREATE TABLE IF NOT EXISTS sessions (
    id                  TEXT PRIMARY KEY,
    prefix_hash         TEXT,
    first_seen          INTEGER NOT NULL,
    last_seen           INTEGER NOT NULL,
    request_count       INTEGER NOT NULL DEFAULT 0,
    total_input_tokens  INTEGER NOT NULL DEFAULT 0,
    total_output_tokens INTEGER NOT NULL DEFAULT 0,
    total_cost_usd      REAL NOT NULL DEFAULT 0,
    priced_count        INTEGER NOT NULL DEFAULT 0,
    unpriced_count      INTEGER NOT NULL DEFAULT 0,
    model_set           TEXT NOT NULL DEFAULT '',
    warning_count       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sessions_prefix_hash ON sessions(prefix_hash);

CREATE TABLE IF NOT EXISTS warnings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    severity   TEXT NOT NULL,
    detail     TEXT NOT NULL,
    path       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    FOREIGN KEY (request_id) REFERENCES requests(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_warnings_request_id ON warnings(request_id);
