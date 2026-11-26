-- +goose Up
CREATE TABLE IF NOT EXISTS board_entries (
    board_id                TEXT NOT NULL,
    entry_id                TEXT NOT NULL,
    submission_id           TEXT,
    sort_value              REAL NOT NULL DEFAULT 0,
    display_json            TEXT NOT NULL,
    created_at_ms           INTEGER NOT NULL,
    removed_at_ms           INTEGER,
    removed_by_operator_id  TEXT,
    PRIMARY KEY (board_id, entry_id)
);

CREATE INDEX IF NOT EXISTS idx_board_active ON board_entries (board_id, removed_at_ms, sort_value DESC, created_at_ms ASC);

-- +goose Down
DROP TABLE IF EXISTS board_entries;
