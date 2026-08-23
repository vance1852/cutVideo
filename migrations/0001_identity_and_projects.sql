CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('editor', 'supervisor', 'auditor')),
    status        TEXT NOT NULL CHECK (status IN ('active', 'suspended')),
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_users_email ON users (email);
CREATE INDEX ix_users_role_status ON users (role, status);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    issued_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    revoked_at   INTEGER
);

CREATE UNIQUE INDEX ux_sessions_token_hash ON sessions (token_hash);
CREATE INDEX ix_sessions_user ON sessions (user_id, expires_at);

CREATE TABLE projects (
    id              TEXT PRIMARY KEY,
    code            TEXT NOT NULL,
    title           TEXT NOT NULL,
    owner_id        TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    status          TEXT NOT NULL CHECK (status IN ('active', 'locked', 'delivered', 'archived')),
    frame_rate      INTEGER NOT NULL,
    resolution      TEXT NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 0,
    sealed_version  INTEGER NOT NULL DEFAULT 0,
    deadline_at     INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_projects_code ON projects (code);
CREATE INDEX ix_projects_owner_status ON projects (owner_id, status);
CREATE INDEX ix_projects_deadline ON projects (deadline_at);

CREATE TABLE media_assets (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    filename         TEXT NOT NULL,
    format           TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('video', 'audio', 'graphics')),
    declared_sum     TEXT NOT NULL,
    checksum         TEXT NOT NULL DEFAULT '',
    bytes            INTEGER NOT NULL,
    duration_ms      INTEGER NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('ingesting', 'verified', 'rejected', 'archived', 'quarantined')),
    reject_reason    TEXT NOT NULL DEFAULT '',
    retention_until  INTEGER NOT NULL,
    ingested_at      INTEGER NOT NULL,
    verified_at      INTEGER,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_assets_project_declared_sum ON media_assets (project_id, declared_sum);
CREATE INDEX ix_assets_project_status ON media_assets (project_id, status);
CREATE INDEX ix_assets_retention ON media_assets (retention_until);
