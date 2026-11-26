-- +goose Up
CREATE TABLE IF NOT EXISTS devices (
    device_id           TEXT PRIMARY KEY,
    public_key          TEXT NOT NULL UNIQUE,
    role                TEXT NOT NULL,
    device_name         TEXT NOT NULL,
    device_type         TEXT NOT NULL,
    manufacturer        TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    os_name             TEXT NOT NULL DEFAULT '',
    os_version          TEXT NOT NULL DEFAULT '',
    app_version         TEXT NOT NULL DEFAULT '',
    framework_version   TEXT NOT NULL DEFAULT '',
    launcher_version    TEXT NOT NULL DEFAULT '',
    game_version        TEXT NOT NULL DEFAULT '',
    protocol_version    INTEGER NOT NULL DEFAULT 1,
    device_info_json    TEXT NOT NULL DEFAULT '{}',
    allow_cloud_relay   INTEGER NOT NULL DEFAULT 1,
    last_ip             TEXT NOT NULL DEFAULT '',
    paired_at_ms        INTEGER NOT NULL,
    last_seen_at_ms     INTEGER NOT NULL,
    revoked_at_ms       INTEGER
);

CREATE TABLE IF NOT EXISTS operators (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    password_hash       TEXT NOT NULL,
    is_default          INTEGER NOT NULL DEFAULT 0,
    token_version       INTEGER NOT NULL DEFAULT 1,
    created_at_ms       INTEGER NOT NULL,
    updated_at_ms       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    revision            INTEGER NOT NULL,
    values_json         TEXT NOT NULL,
    updated_at_ms       INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS operators;
DROP TABLE IF EXISTS devices;
