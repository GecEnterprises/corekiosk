-- +goose Up
CREATE TABLE IF NOT EXISTS ingested_events (
    event_id         TEXT PRIMARY KEY,
    installation_id  TEXT NOT NULL,
    app_name         TEXT NOT NULL,
    origin_device_id TEXT NOT NULL,
    origin_role      TEXT NOT NULL,
    category         TEXT NOT NULL,
    kind             TEXT NOT NULL,
    severity         TEXT NOT NULL,
    occurred_at_ms   INTEGER NOT NULL,
    received_at_ms   INTEGER NOT NULL,
    payload_json     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ingested_install_time ON ingested_events (installation_id, occurred_at_ms DESC);

-- +goose Down
DROP TABLE IF EXISTS ingested_events;
