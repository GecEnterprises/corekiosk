-- +goose Up
CREATE TABLE IF NOT EXISTS assets (
    id            TEXT PRIMARY KEY,
    sha256        TEXT NOT NULL UNIQUE,
    filename      TEXT NOT NULL DEFAULT '',
    mime_type     TEXT NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    size_bytes    INTEGER NOT NULL,
    seed_key      TEXT NOT NULL DEFAULT '',
    data          BLOB NOT NULL,
    created_at_ms INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_assets_created ON assets (created_at_ms DESC);

CREATE TABLE IF NOT EXISTS catalogs_state (
    catalog_id    TEXT PRIMARY KEY,
    revision      INTEGER NOT NULL DEFAULT 1,
    entries_json  TEXT NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rulesets_state (
    ruleset_id    TEXT PRIMARY KEY,
    revision      INTEGER NOT NULL DEFAULT 1,
    rules_json    TEXT NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS rulesets_state;
DROP TABLE IF EXISTS catalogs_state;
DROP TABLE IF EXISTS assets;
