-- +goose Up
CREATE TABLE IF NOT EXISTS submissions (
    id                  TEXT PRIMARY KEY,
    request_id          TEXT NOT NULL UNIQUE,
    form_id             TEXT NOT NULL,
    form_version        INTEGER NOT NULL DEFAULT 1,
    payload_json        TEXT NOT NULL,
    source_device_id    TEXT NOT NULL,
    round_id            TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'submitted',
    outcome_json        TEXT NOT NULL DEFAULT '{}',
    created_at_ms       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_submissions_created ON submissions (created_at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_submissions_form ON submissions (form_id, created_at_ms DESC);

CREATE TABLE IF NOT EXISTS rounds (
    id                  TEXT PRIMARY KEY,
    submission_id       TEXT NOT NULL REFERENCES submissions(id),
    request_id          TEXT NOT NULL UNIQUE,
    form_id             TEXT NOT NULL,
    state               TEXT NOT NULL,
    score               INTEGER NOT NULL DEFAULT 0,
    finish_reason       TEXT NOT NULL DEFAULT '',
    config_revision     INTEGER NOT NULL DEFAULT 0,
    created_at_ms       INTEGER NOT NULL,
    started_at_ms       INTEGER NOT NULL DEFAULT 0,
    finished_at_ms      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_rounds_state ON rounds (state, score DESC, finished_at_ms ASC);

-- +goose Down
DROP TABLE IF EXISTS rounds;
DROP TABLE IF EXISTS submissions;
