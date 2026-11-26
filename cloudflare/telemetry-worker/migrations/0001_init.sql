-- Corekiosk Serverless Telemetry & Form-Sync Schema (Cloudflare D1 / SQLite)

CREATE TABLE IF NOT EXISTS installations (
    installation_id TEXT PRIMARY KEY,
    app_name        TEXT NOT NULL DEFAULT '',
    last_sender_id  TEXT NOT NULL DEFAULT '',
    last_seen_ms    INTEGER NOT NULL,
    created_at_ms   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    installation_id       TEXT NOT NULL,
    device_id             TEXT NOT NULL,
    public_key            TEXT NOT NULL DEFAULT '',
    role                  TEXT NOT NULL DEFAULT '',
    device_name           TEXT NOT NULL DEFAULT '',
    device_type           TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    os_name               TEXT NOT NULL DEFAULT '',
    os_version            TEXT NOT NULL DEFAULT '',
    app_version           TEXT NOT NULL DEFAULT '',
    state_json            TEXT NOT NULL DEFAULT '{}',
    origin_boot_id        TEXT NOT NULL DEFAULT '',
    origin_seq            INTEGER NOT NULL DEFAULT 0,
    origin_occurred_at_ms INTEGER NOT NULL DEFAULT 0,
    updated_at_ms         INTEGER NOT NULL,
    PRIMARY KEY (installation_id, device_id)
);

CREATE TABLE IF NOT EXISTS form_schemas (
    installation_id TEXT NOT NULL,
    form_id         TEXT NOT NULL,
    schema_json     TEXT NOT NULL,
    updated_at_ms   INTEGER NOT NULL,
    PRIMARY KEY (installation_id, form_id)
);

CREATE TABLE IF NOT EXISTS form_submissions (
    submission_id      TEXT PRIMARY KEY,
    installation_id    TEXT NOT NULL,
    app_name           TEXT NOT NULL DEFAULT '',
    form_id            TEXT NOT NULL DEFAULT 'registration',
    form_version       INTEGER NOT NULL DEFAULT 1,
    source_device_id   TEXT NOT NULL DEFAULT '',
    source_device_name TEXT NOT NULL DEFAULT '',
    round_id           TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'submitted',
    score              INTEGER,
    reward_id          TEXT NOT NULL DEFAULT '',
    reward_title       TEXT NOT NULL DEFAULT '',
    fields_json        TEXT NOT NULL DEFAULT '{}',
    outcome_json       TEXT NOT NULL DEFAULT '{}',
    created_at_ms      INTEGER NOT NULL,
    updated_at_ms      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_form_sub_install_created
    ON form_submissions (installation_id, created_at_ms DESC);

CREATE INDEX IF NOT EXISTS idx_form_sub_install_reward
    ON form_submissions (installation_id, reward_title);

CREATE TABLE IF NOT EXISTS board_entries (
    installation_id        TEXT NOT NULL,
    board_id               TEXT NOT NULL,
    entry_id               TEXT NOT NULL,
    submission_id          TEXT NOT NULL DEFAULT '',
    sort_value             REAL NOT NULL DEFAULT 0,
    display_json           TEXT NOT NULL DEFAULT '{}',
    created_at_ms          INTEGER NOT NULL DEFAULT 0,
    removed_at_ms          INTEGER,
    removed_by_operator_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (installation_id, board_id, entry_id)
);

CREATE INDEX IF NOT EXISTS idx_board_active
    ON board_entries (installation_id, board_id, removed_at_ms, sort_value DESC);

CREATE TABLE IF NOT EXISTS events (
    event_id         TEXT PRIMARY KEY,
    installation_id  TEXT NOT NULL,
    app_name         TEXT NOT NULL DEFAULT '',
    origin_device_id TEXT NOT NULL,
    origin_role      TEXT NOT NULL,
    origin_boot_id   TEXT NOT NULL DEFAULT '',
    origin_seq       INTEGER NOT NULL DEFAULT 0,
    category         TEXT NOT NULL,
    kind             TEXT NOT NULL,
    severity         TEXT NOT NULL DEFAULT 'info',
    occurred_at_ms   INTEGER NOT NULL,
    received_at_ms   INTEGER NOT NULL,
    relayed_from     TEXT NOT NULL DEFAULT '',
    payload_json     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_events_install_time
    ON events (installation_id, occurred_at_ms DESC);

CREATE INDEX IF NOT EXISTS idx_events_install_cat
    ON events (installation_id, category, occurred_at_ms DESC);
