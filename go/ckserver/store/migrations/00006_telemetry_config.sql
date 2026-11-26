-- +goose Up
CREATE TABLE IF NOT EXISTS telemetry_config (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    ingest_url        TEXT NOT NULL DEFAULT '',
    api_key           TEXT NOT NULL DEFAULT '',
    flush_interval_s  INTEGER NOT NULL DEFAULT 15,
    batch_size        INTEGER NOT NULL DEFAULT 100,
    updated_at_ms     INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS telemetry_config;
