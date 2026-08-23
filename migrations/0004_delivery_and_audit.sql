CREATE TABLE delivery_targets (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('webhook', 's3', 'aspera')),
    endpoint       TEXT NOT NULL,
    credential_ref TEXT NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_targets_project_name ON delivery_targets (project_id, name);
CREATE INDEX ix_targets_project_status ON delivery_targets (project_id, status);

CREATE TABLE delivery_records (
    id            TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES render_jobs (id) ON DELETE CASCADE,
    target_id     TEXT NOT NULL REFERENCES delivery_targets (id) ON DELETE CASCADE,
    project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    status        TEXT NOT NULL CHECK (status IN ('pending', 'dispatched', 'confirmed', 'failed', 'skipped')),
    attempt       INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL,
    output_uri    TEXT NOT NULL,
    failure       TEXT NOT NULL DEFAULT '',
    dispatched_at INTEGER,
    confirmed_at  INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_delivery_job_target ON delivery_records (job_id, target_id);
CREATE INDEX ix_delivery_project_status ON delivery_records (project_id, status);

CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT NOT NULL DEFAULT '',
    actor_role  TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    object_kind TEXT NOT NULL,
    object_id   TEXT NOT NULL DEFAULT '',
    result      TEXT NOT NULL CHECK (result IN ('succeeded', 'rejected', 'failed')),
    request_id  TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE INDEX ix_audit_object ON audit_events (object_kind, object_id, created_at);
CREATE INDEX ix_audit_actor ON audit_events (actor_id, created_at);
CREATE INDEX ix_audit_action ON audit_events (action, created_at);

CREATE TABLE idempotency_keys (
    id            TEXT PRIMARY KEY,
    scope         TEXT NOT NULL,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    actor_id      TEXT NOT NULL DEFAULT '',
    key           TEXT NOT NULL,
    request_hash  TEXT NOT NULL,
    response_code INTEGER NOT NULL,
    response_body TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_idempotency_scope_key ON idempotency_keys (scope, method, path, actor_id, key);
CREATE INDEX ix_idempotency_expiry ON idempotency_keys (expires_at);
