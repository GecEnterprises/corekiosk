-- +goose Up
CREATE TABLE IF NOT EXISTS telemetry_outbox (
    seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id            TEXT NOT NULL UNIQUE,
    origin_device_id    TEXT NOT NULL,
    origin_role         TEXT NOT NULL,
    origin_boot_id      TEXT NOT NULL,
    origin_seq          INTEGER NOT NULL,
    category            TEXT NOT NULL,
    kind                TEXT NOT NULL,
    severity            TEXT NOT NULL DEFAULT 'info',
    occurred_at_ms      INTEGER NOT NULL,
    payload_json        TEXT NOT NULL,
    relayed_from        TEXT,
    cloud_synced_at_ms  INTEGER,
    sync_attempts       INTEGER NOT NULL DEFAULT 0,
    last_sync_error     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_outbox_unsynced ON telemetry_outbox (cloud_synced_at_ms, seq);
CREATE INDEX IF NOT EXISTS idx_outbox_origin   ON telemetry_outbox (origin_device_id, origin_seq);
CREATE INDEX IF NOT EXISTS idx_outbox_category ON telemetry_outbox (category, occurred_at_ms DESC);

CREATE TABLE IF NOT EXISTS telemetry_peer_cursors (
    peer_device_id      TEXT PRIMARY KEY,
    last_pulled_seq     INTEGER NOT NULL DEFAULT 0,
    last_acked_seq      INTEGER NOT NULL DEFAULT 0,
    updated_at_ms       INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS telemetry_peer_cursors;
DROP TABLE IF EXISTS telemetry_outbox;
