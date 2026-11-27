-- +goose Up
CREATE TABLE IF NOT EXISTS launcher_config (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    auto_launch          INTEGER NOT NULL DEFAULT 1,
    countdown_seconds    INTEGER NOT NULL DEFAULT 30,
    auto_restart_on_exit INTEGER NOT NULL DEFAULT 1,
    open_companion       INTEGER NOT NULL DEFAULT 0,
    updated_at_ms        INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS launcher_config;
