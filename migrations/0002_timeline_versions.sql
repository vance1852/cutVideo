CREATE TABLE timeline_versions (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    version           INTEGER NOT NULL,
    status            TEXT NOT NULL CHECK (status IN ('draft', 'sealed', 'superseded')),
    notes             TEXT NOT NULL DEFAULT '',
    created_by        TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    clip_count        INTEGER NOT NULL DEFAULT 0,
    total_duration_ms INTEGER NOT NULL DEFAULT 0,
    row_version       INTEGER NOT NULL DEFAULT 1,
    sealed_at         INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE UNIQUE INDEX ux_timeline_project_version ON timeline_versions (project_id, version);
CREATE INDEX ix_timeline_project_status ON timeline_versions (project_id, status);

CREATE TABLE timeline_clips (
    id            TEXT PRIMARY KEY,
    timeline_id   TEXT NOT NULL REFERENCES timeline_versions (id) ON DELETE CASCADE,
    asset_id      TEXT NOT NULL REFERENCES media_assets (id) ON DELETE RESTRICT,
    order_index   INTEGER NOT NULL,
    source_in_ms  INTEGER NOT NULL,
    source_out_ms INTEGER NOT NULL,
    track         TEXT NOT NULL CHECK (track IN ('program', 'broll', 'audio', 'titles')),
    transition    TEXT NOT NULL DEFAULT '',
    speed_percent INTEGER NOT NULL DEFAULT 100,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (source_out_ms > source_in_ms)
);

CREATE UNIQUE INDEX ux_clips_timeline_track_order ON timeline_clips (timeline_id, track, order_index);
CREATE INDEX ix_clips_asset ON timeline_clips (asset_id);
