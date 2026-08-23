CREATE TABLE render_slots (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    pool            TEXT NOT NULL,
    units           INTEGER NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('idle', 'busy', 'draining', 'offline')),
    held_by_job_id  TEXT NOT NULL DEFAULT '',
    leased_at       INTEGER,
    released_at     INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_slots_name ON render_slots (name);
CREATE INDEX ix_slots_pool_status ON render_slots (pool, status);

CREATE TABLE render_jobs (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    timeline_id      TEXT NOT NULL REFERENCES timeline_versions (id) ON DELETE RESTRICT,
    requested_by     TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    preset           TEXT NOT NULL,
    priority         INTEGER NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('queued', 'assigned', 'rendering', 'succeeded', 'failed', 'canceled')),
    attempt          INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL,
    slot_id          TEXT NOT NULL DEFAULT '',
    lease_expires_at INTEGER,
    next_attempt_at  INTEGER NOT NULL,
    queued_at        INTEGER NOT NULL,
    started_at       INTEGER,
    finished_at      INTEGER,
    last_error       TEXT NOT NULL DEFAULT '',
    output_uri       TEXT NOT NULL DEFAULT '',
    output_bytes     INTEGER NOT NULL DEFAULT 0,
    idempotency_key  TEXT NOT NULL DEFAULT '',
    row_version      INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE INDEX ix_render_queue ON render_jobs (status, next_attempt_at, priority);
CREATE INDEX ix_render_project ON render_jobs (project_id, status);
CREATE INDEX ix_render_timeline ON render_jobs (timeline_id, status);
CREATE INDEX ix_render_lease ON render_jobs (status, lease_expires_at);
CREATE UNIQUE INDEX ux_render_idempotency ON render_jobs (project_id, idempotency_key)
    WHERE idempotency_key <> '';
