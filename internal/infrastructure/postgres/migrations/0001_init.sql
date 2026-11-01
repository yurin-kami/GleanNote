/*
 * GleanNote 初始 schema（PostgreSQL 16）。
 *
 * 约定：所有业务表以 user_id 作为隔离边界，仓储层每条查询都显式带上该列（G-08）；
 * 时间统一使用 timestamptz；软删除以 deleted_at 标记，回收站保留期由用例计算。
 * 迁移可重复执行（IF NOT EXISTS），启动时由适配器自动应用。
 */

CREATE TABLE IF NOT EXISTS users (
    id                TEXT PRIMARY KEY,
    email             TEXT NOT NULL,
    password_hash     TEXT NOT NULL,
    nickname          TEXT NOT NULL,
    status            TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    deleted_at        TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_users_email ON users (lower(email));

CREATE TABLE IF NOT EXISTS email_codes (
    id          TEXT PRIMARY KEY,
    email       TEXT NOT NULL,
    purpose     TEXT NOT NULL,
    code_hash   TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_email_codes_lookup ON email_codes (lower(email), purpose, created_at DESC);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    token_hash  TEXT NOT NULL,
    device_id   TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    replaced_by TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_refresh_tokens_hash ON refresh_tokens (token_hash);
CREATE INDEX IF NOT EXISTS ix_refresh_tokens_user ON refresh_tokens (user_id, device_id);

CREATE TABLE IF NOT EXISTS folders (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    parent_id       TEXT,
    name            TEXT NOT NULL,
    depth           INTEGER NOT NULL,
    path            TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    deleted_at      TIMESTAMPTZ,
    delete_batch_id TEXT NOT NULL DEFAULT '',
    purge_at        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_folders_sibling_name
    ON folders (user_id, COALESCE(parent_id, ''), lower(name)) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS ix_folders_user_parent ON folders (user_id, parent_id);
CREATE INDEX IF NOT EXISTS ix_folders_user_path ON folders (user_id, path);

CREATE TABLE IF NOT EXISTS notes (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    folder_id       TEXT NOT NULL,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL,
    state           TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    char_count      INTEGER NOT NULL,
    conflict_copy   BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at      TIMESTAMPTZ,
    delete_batch_id TEXT NOT NULL DEFAULT '',
    purge_at        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_notes_folder_updated ON notes (user_id, folder_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_notes_folder_created ON notes (user_id, folder_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_notes_user_deleted ON notes (user_id, deleted_at);
CREATE INDEX IF NOT EXISTS ix_notes_batch ON notes (user_id, delete_batch_id);

CREATE TABLE IF NOT EXISTS note_pages (
    id         TEXT PRIMARY KEY,
    note_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    page_index INTEGER NOT NULL,
    object_key TEXT NOT NULL,
    thumb_key  TEXT NOT NULL DEFAULT '',
    bytes      BIGINT NOT NULL,
    width      INTEGER NOT NULL,
    height     INTEGER NOT NULL,
    sha256     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_note_pages_note ON note_pages (user_id, note_id, page_index);

CREATE TABLE IF NOT EXISTS uploads (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    object_key   TEXT NOT NULL,
    file_name    TEXT NOT NULL,
    content_type TEXT NOT NULL,
    sha256       TEXT NOT NULL DEFAULT '',
    bytes        BIGINT NOT NULL,
    width        INTEGER NOT NULL DEFAULT 0,
    height       INTEGER NOT NULL DEFAULT 0,
    thumb_key    TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_uploads_user ON uploads (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS conversion_jobs (
    id                   TEXT PRIMARY KEY,
    note_id              TEXT NOT NULL,
    user_id              TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    state                TEXT NOT NULL,
    phase                TEXT NOT NULL,
    total_pages          INTEGER NOT NULL,
    processed_pages      INTEGER NOT NULL DEFAULT 0,
    failure_code         TEXT NOT NULL DEFAULT '',
    failure_user_message TEXT NOT NULL DEFAULT '',
    quota_charged        INTEGER NOT NULL DEFAULT 0,
    attempt              INTEGER NOT NULL DEFAULT 1,
    created_at           TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL,
    started_at           TIMESTAMPTZ,
    finished_at          TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_conversion_jobs_note ON conversion_jobs (user_id, note_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_conversion_jobs_active ON conversion_jobs (user_id, state);

CREATE TABLE IF NOT EXISTS quota_usage (
    user_id    TEXT NOT NULL,
    quota_date TEXT NOT NULL,
    used       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, quota_date)
);

CREATE TABLE IF NOT EXISTS quota_charges (
    user_id     TEXT NOT NULL,
    job_id      TEXT NOT NULL,
    quota_date  TEXT NOT NULL,
    amount      INTEGER NOT NULL,
    refunded_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, job_id)
);

CREATE TABLE IF NOT EXISTS storage_usage (
    user_id    TEXT PRIMARY KEY,
    used_bytes BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tags (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_tags_user_name ON tags (user_id, lower(name));

CREATE TABLE IF NOT EXISTS note_tags (
    user_id TEXT NOT NULL,
    note_id TEXT NOT NULL,
    tag_id  TEXT NOT NULL,
    PRIMARY KEY (note_id, tag_id)
);
CREATE INDEX IF NOT EXISTS ix_note_tags_tag ON note_tags (user_id, tag_id);

CREATE TABLE IF NOT EXISTS note_versions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    note_id    TEXT NOT NULL,
    source     TEXT NOT NULL,
    content    TEXT NOT NULL,
    char_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_note_versions_note ON note_versions (user_id, note_id, created_at DESC);

CREATE TABLE IF NOT EXISTS share_links (
    user_id    TEXT NOT NULL,
    note_id    TEXT NOT NULL,
    token      TEXT NOT NULL DEFAULT '',
    enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, note_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_share_links_token ON share_links (token) WHERE token <> '';

CREATE TABLE IF NOT EXISTS export_jobs (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    note_id      TEXT NOT NULL DEFAULT '',
    folder_id    TEXT NOT NULL DEFAULT '',
    format       TEXT NOT NULL,
    state        TEXT NOT NULL,
    object_key   TEXT NOT NULL DEFAULT '',
    failure_code TEXT NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_export_jobs_user ON export_jobs (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS import_jobs (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    folder_id    TEXT NOT NULL,
    mode         TEXT NOT NULL,
    state        TEXT NOT NULL,
    total        INTEGER NOT NULL,
    succeeded    INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    failure_code TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    finished_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_import_jobs_user ON import_jobs (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS import_items (
    job_id       TEXT NOT NULL,
    position     INTEGER NOT NULL,
    upload_id    TEXT NOT NULL,
    note_id      TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL,
    failure_code TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, position)
);

CREATE TABLE IF NOT EXISTS change_events (
    user_id       TEXT NOT NULL,
    seq           BIGINT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    op            TEXT NOT NULL,
    revision      INTEGER NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL,
    payload       JSONB,
    PRIMARY KEY (user_id, seq)
);
CREATE INDEX IF NOT EXISTS ix_change_events_time ON change_events (updated_at);

CREATE TABLE IF NOT EXISTS change_seq (
    user_id  TEXT PRIMARY KEY,
    next_seq BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS change_watermarks (
    user_id         TEXT PRIMARY KEY,
    trimmed_through BIGINT NOT NULL DEFAULT 0
);
