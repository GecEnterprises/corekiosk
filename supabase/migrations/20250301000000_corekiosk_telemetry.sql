-- =============================================================================
-- corekiosk — Supabase (PostgreSQL) Telemetry, Lead Capture & Prize Tally Schema
-- Paste this entire file into the Supabase Dashboard -> SQL Editor and click Run.
-- =============================================================================

-- 1. Installations (1 row per kiosk PC server_id)
CREATE TABLE IF NOT EXISTS public.installations (
    installation_id   TEXT PRIMARY KEY,
    app_name          TEXT NOT NULL DEFAULT '',
    venue             TEXT NOT NULL DEFAULT '',
    active_phase      TEXT NOT NULL DEFAULT '',
    versions_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_ms     BIGINT NOT NULL,
    last_seen_ms      BIGINT NOT NULL
);

-- 2. Paired Devices + Kiosk PC hardware profile & live status (monotonic LWW)
CREATE TABLE IF NOT EXISTS public.devices (
    installation_id   TEXT NOT NULL,
    device_id         TEXT NOT NULL,
    public_key        TEXT NOT NULL DEFAULT '',
    role              TEXT NOT NULL DEFAULT '',
    device_name       TEXT NOT NULL DEFAULT '',
    device_type       TEXT NOT NULL DEFAULT '',
    manufacturer      TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    os_name           TEXT NOT NULL DEFAULT '',
    os_version        TEXT NOT NULL DEFAULT '',
    app_version       TEXT NOT NULL DEFAULT '',
    framework_version TEXT NOT NULL DEFAULT '',
    launcher_version  TEXT NOT NULL DEFAULT '',
    game_version      TEXT NOT NULL DEFAULT '',
    protocol_version  INTEGER NOT NULL DEFAULT 1,
    battery_pct       DOUBLE PRECISION,
    is_charging       BOOLEAN,
    temp_c            DOUBLE PRECISION,
    ssid              TEXT,
    bssid             TEXT,
    rssi_dbm          INTEGER,
    ip                TEXT,
    ws_connected      BOOLEAN,
    game_running      BOOLEAN,
    origin_boot_ts_ms BIGINT NOT NULL DEFAULT 0,
    origin_seq        BIGINT NOT NULL DEFAULT 0,
    revoked_at_ms     BIGINT,
    updated_at_ms     BIGINT NOT NULL,
    raw_json          JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (installation_id, device_id)
);

CREATE INDEX IF NOT EXISTS idx_devices_lookup ON public.devices (device_id);

-- 3. Form Schemas
CREATE TABLE IF NOT EXISTS public.form_schemas (
    installation_id   TEXT NOT NULL,
    form_id           TEXT NOT NULL,
    form_version      INTEGER NOT NULL DEFAULT 1,
    schema_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at_ms     BIGINT NOT NULL,
    PRIMARY KEY (installation_id, form_id, form_version)
);

-- 4. Form Submissions (Player Leads + Out-of-Order Safe Outcome & Prize Merge)
CREATE TABLE IF NOT EXISTS public.form_submissions (
    installation_id    TEXT NOT NULL,
    submission_id      TEXT NOT NULL,
    form_id            TEXT NOT NULL DEFAULT 'registration',
    form_version       INTEGER NOT NULL DEFAULT 1,
    source_device_id   TEXT NOT NULL DEFAULT '',
    source_device_name TEXT NOT NULL DEFAULT '',
    round_id           TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'submitted',
    fields_json        JSONB NOT NULL DEFAULT '{}'::jsonb,
    outcome_json       JSONB,
    score              DOUBLE PRECISION,
    reward_id          TEXT,
    reward_title       TEXT,
    created_at_ms      BIGINT NOT NULL,
    updated_at_ms      BIGINT NOT NULL,
    PRIMARY KEY (installation_id, submission_id)
);

CREATE INDEX IF NOT EXISTS idx_submissions_created
    ON public.form_submissions (installation_id, form_id, created_at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_submissions_reward
    ON public.form_submissions (installation_id, reward_id, created_at_ms DESC);

-- 5. Curated Boards / Leaderboards (2P-Set CRDT: tombstone always wins)
CREATE TABLE IF NOT EXISTS public.board_entries (
    installation_id        TEXT NOT NULL,
    board_id               TEXT NOT NULL,
    entry_id               TEXT NOT NULL,
    submission_id          TEXT NOT NULL DEFAULT '',
    sort_value             DOUBLE PRECISION NOT NULL DEFAULT 0,
    display_json           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at_ms          BIGINT NOT NULL,
    removed_at_ms          BIGINT,
    removed_by_operator_id TEXT,
    updated_at_ms          BIGINT NOT NULL,
    PRIMARY KEY (installation_id, board_id, entry_id)
);

CREATE INDEX IF NOT EXISTS idx_board_active
    ON public.board_entries (installation_id, board_id, removed_at_ms, sort_value DESC);

-- 6. Immutable Events & Metric Chunks (Alerts, Audits, Wi-Fi/Battery/Vitals Chunks)
CREATE TABLE IF NOT EXISTS public.events (
    event_id          TEXT PRIMARY KEY,
    installation_id   TEXT NOT NULL,
    origin_device_id  TEXT NOT NULL,
    origin_role       TEXT NOT NULL,
    origin_boot_id    TEXT NOT NULL DEFAULT '',
    origin_seq        BIGINT NOT NULL DEFAULT 0,
    category          TEXT NOT NULL,
    kind              TEXT NOT NULL,
    severity          TEXT NOT NULL DEFAULT 'info',
    occurred_at_ms    BIGINT NOT NULL,
    relayed_from      TEXT,
    received_at_ms    BIGINT NOT NULL,
    payload_json      JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_events_install_time
    ON public.events (installation_id, occurred_at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_events_category
    ON public.events (installation_id, category, occurred_at_ms DESC);

-- 7. Convenience Views for the Supabase Table Editor / CSV Export
CREATE OR REPLACE VIEW public.player_leads_view AS
SELECT
    installation_id,
    submission_id,
    to_timestamp(created_at_ms / 1000.0) AS created_at,
    source_device_name,
    status,
    fields_json ->> 'name'        AS player_name,
    fields_json ->> 'ig_username' AS ig_username,
    fields_json ->> 'email'       AS email,
    fields_json ->> 'phone'       AS phone,
    score,
    reward_id,
    reward_title,
    round_id,
    fields_json,
    outcome_json
FROM public.form_submissions
ORDER BY created_at_ms DESC;

CREATE OR REPLACE VIEW public.prize_tallies_view AS
SELECT
    installation_id,
    COALESCE(reward_id, 'none')          AS reward_id,
    COALESCE(reward_title, 'Unresolved') AS reward_title,
    COUNT(*)                             AS total_awarded,
    ROUND(AVG(score)::numeric, 1)        AS avg_score,
    MIN(score)                           AS min_score,
    MAX(score)                           AS max_score
FROM public.form_submissions
GROUP BY installation_id, COALESCE(reward_id, 'none'), COALESCE(reward_title, 'Unresolved')
ORDER BY total_awarded DESC;

-- Enable RLS so direct public anon access is blocked while service_role (Edge Function) has full access
ALTER TABLE public.installations    ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.devices          ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.form_schemas     ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.form_submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.board_entries    ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.events           ENABLE ROW LEVEL SECURITY;

-- =============================================================================
-- 8. Single-Roundtrip Atomic Ingest RPC: public.ck_ingest_batch
-- Called by the Supabase Edge Function after Ed25519 signature verification.
-- =============================================================================
CREATE OR REPLACE FUNCTION public.ck_ingest_batch(
    p_batch jsonb,
    p_sender_public_key text DEFAULT ''
)
RETURNS jsonb
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_installation_id text := COALESCE(NULLIF(trim(p_batch ->> 'installation_id'), ''), trim(p_batch ->> 'sender_device_id'));
    v_app_name        text := COALESCE(p_batch ->> 'app_name', '');
    v_sender_id       text := COALESCE(p_batch ->> 'sender_device_id', '');
    v_now_ms          bigint := (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint;
    v_existing_pub    text;
    v_ev              jsonb;
    v_payload         jsonb;
    v_ev_id           text;
    v_kind            text;
    v_cat             text;
    v_origin_id       text;
    v_origin_role     text;
    v_origin_boot     text;
    v_origin_seq      bigint;
    v_occurred_ms     bigint;
    v_sub_id          text;
    v_form_id         text;
    v_form_ver        integer;
    v_outcome         jsonb;
    v_score           double precision;
    v_reward_id       text;
    v_reward_title    text;
    v_accepted_ids    text[] := ARRAY[]::text[];
BEGIN
    IF v_installation_id = '' OR v_sender_id = '' THEN
        RETURN jsonb_build_object('ok', false, 'status', 400, 'error', 'missing installation_id or sender_device_id');
    END IF;

    -- TOFU key check: if sender device already has a pinned public key, verify it matches
    SELECT d.public_key INTO v_existing_pub
    FROM public.devices d
    WHERE d.installation_id = v_installation_id AND d.device_id = v_sender_id AND d.public_key <> ''
    LIMIT 1;

    IF v_existing_pub IS NOT NULL AND p_sender_public_key <> '' AND v_existing_pub <> p_sender_public_key THEN
        RETURN jsonb_build_object('ok', false, 'status', 401, 'error', 'sender public key mismatch against pinned device key');
    END IF;

    -- Upsert installation record
    INSERT INTO public.installations (installation_id, app_name, first_seen_ms, last_seen_ms)
    VALUES (v_installation_id, v_app_name, v_now_ms, v_now_ms)
    ON CONFLICT (installation_id) DO UPDATE SET
        app_name     = CASE WHEN EXCLUDED.app_name <> '' THEN EXCLUDED.app_name ELSE public.installations.app_name END,
        last_seen_ms = GREATEST(public.installations.last_seen_ms, EXCLUDED.last_seen_ms);

    -- Pin sender's public key on first sight (TOFU)
    IF p_sender_public_key <> '' THEN
        INSERT INTO public.devices (installation_id, device_id, public_key, role, updated_at_ms)
        VALUES (
            v_installation_id,
            v_sender_id,
            p_sender_public_key,
            CASE WHEN v_sender_id = v_installation_id THEN 'server' ELSE 'tablet' END,
            v_now_ms
        )
        ON CONFLICT (installation_id, device_id) DO UPDATE SET
            public_key    = CASE WHEN public.devices.public_key = '' THEN EXCLUDED.public_key ELSE public.devices.public_key END,
            updated_at_ms = GREATEST(public.devices.updated_at_ms, EXCLUDED.updated_at_ms);
    END IF;

    -- Process each event in the batch
    FOR v_ev IN SELECT * FROM jsonb_array_elements(COALESCE(p_batch -> 'events', '[]'::jsonb))
    LOOP
        v_ev_id       := COALESCE(trim(v_ev ->> 'event_id'), '');
        v_kind        := COALESCE(trim(v_ev ->> 'kind'), '');
        IF v_ev_id = '' OR v_kind = '' THEN
            CONTINUE;
        END IF;

        v_accepted_ids := array_append(v_accepted_ids, v_ev_id);
        v_cat          := COALESCE(NULLIF(trim(v_ev ->> 'category'), ''), 'system');
        v_origin_id    := COALESCE(NULLIF(trim(v_ev ->> 'origin_device_id'), ''), v_sender_id);
        v_origin_role  := COALESCE(NULLIF(trim(v_ev ->> 'origin_role'), ''), 'server');
        v_origin_boot  := COALESCE(v_ev ->> 'origin_boot_id', '');
        v_origin_seq   := COALESCE((v_ev ->> 'origin_seq')::bigint, 0);
        v_occurred_ms  := COALESCE((v_ev ->> 'occurred_at_ms')::bigint, v_now_ms);
        v_payload      := COALESCE(v_ev -> 'payload', '{}'::jsonb);

        -- A. Form Submission (player lead capture)
        IF v_kind = 'form.submission' THEN
            v_sub_id   := COALESCE(NULLIF(v_payload ->> 'submission_id', ''), v_ev_id);
            v_form_id  := COALESCE(NULLIF(v_payload ->> 'form_id', ''), 'registration');
            v_form_ver := COALESCE((v_payload ->> 'form_version')::integer, 1);
            v_outcome  := v_payload -> 'outcome';

            IF jsonb_typeof(v_payload -> 'form_schema') = 'object' THEN
                INSERT INTO public.form_schemas (installation_id, form_id, form_version, schema_json, updated_at_ms)
                VALUES (v_installation_id, v_form_id, v_form_ver, v_payload -> 'form_schema', v_occurred_ms)
                ON CONFLICT (installation_id, form_id, form_version) DO NOTHING;
            END IF;

            v_score        := CASE WHEN jsonb_typeof(v_outcome -> 'score') = 'number' THEN (v_outcome ->> 'score')::double precision ELSE NULL END;
            v_reward_id    := NULLIF(COALESCE(v_outcome ->> 'reward_id', v_outcome ->> 'prize_id', ''), '');
            v_reward_title := NULLIF(COALESCE(v_outcome ->> 'reward_title', v_outcome ->> 'prize_name', v_outcome ->> 'tier_label', ''), '');

            INSERT INTO public.form_submissions (
                installation_id, submission_id, form_id, form_version,
                source_device_id, source_device_name, round_id, status,
                fields_json, outcome_json, score, reward_id, reward_title,
                created_at_ms, updated_at_ms
            ) VALUES (
                v_installation_id,
                v_sub_id,
                v_form_id,
                v_form_ver,
                COALESCE(NULLIF(v_payload ->> 'source_device_id', ''), v_origin_id),
                COALESCE(v_payload ->> 'source_device_name', ''),
                COALESCE(v_payload ->> 'round_id', ''),
                COALESCE(NULLIF(v_payload ->> 'status', ''), 'submitted'),
                COALESCE(v_payload -> 'fields', '{}'::jsonb),
                CASE WHEN jsonb_typeof(v_outcome) = 'object' THEN v_outcome ELSE NULL END,
                v_score,
                v_reward_id,
                v_reward_title,
                v_occurred_ms,
                v_now_ms
            )
            ON CONFLICT (installation_id, submission_id) DO UPDATE SET
                form_id            = CASE WHEN public.form_submissions.form_id = '' THEN EXCLUDED.form_id ELSE public.form_submissions.form_id END,
                source_device_id   = CASE WHEN public.form_submissions.source_device_id = '' THEN EXCLUDED.source_device_id ELSE public.form_submissions.source_device_id END,
                source_device_name = CASE WHEN public.form_submissions.source_device_name = '' THEN EXCLUDED.source_device_name ELSE public.form_submissions.source_device_name END,
                round_id           = CASE WHEN public.form_submissions.round_id = '' THEN EXCLUDED.round_id ELSE public.form_submissions.round_id END,
                fields_json        = CASE WHEN public.form_submissions.fields_json = '{}'::jsonb THEN EXCLUDED.fields_json ELSE public.form_submissions.fields_json END,
                outcome_json       = COALESCE(public.form_submissions.outcome_json, EXCLUDED.outcome_json),
                score              = COALESCE(public.form_submissions.score, EXCLUDED.score),
                reward_id          = COALESCE(public.form_submissions.reward_id, EXCLUDED.reward_id),
                reward_title       = COALESCE(public.form_submissions.reward_title, EXCLUDED.reward_title),
                created_at_ms      = LEAST(public.form_submissions.created_at_ms, EXCLUDED.created_at_ms),
                updated_at_ms      = GREATEST(public.form_submissions.updated_at_ms, EXCLUDED.updated_at_ms);

        -- B. Form Outcome (round finished -> score & prize tier resolved; safe if arrives before form.submission)
        ELSIF v_kind = 'form.outcome' THEN
            v_sub_id   := COALESCE(v_payload ->> 'submission_id', '');
            v_form_id  := COALESCE(NULLIF(v_payload ->> 'form_id', ''), 'registration');
            v_outcome  := COALESCE(v_payload -> 'outcome', '{}'::jsonb);
            IF v_sub_id <> '' THEN
                v_score        := CASE WHEN jsonb_typeof(v_outcome -> 'score') = 'number' THEN (v_outcome ->> 'score')::double precision ELSE NULL END;
                v_reward_id    := NULLIF(COALESCE(v_outcome ->> 'reward_id', v_outcome ->> 'prize_id', ''), '');
                v_reward_title := NULLIF(COALESCE(v_outcome ->> 'reward_title', v_outcome ->> 'prize_name', v_outcome ->> 'tier_label', ''), '');

                INSERT INTO public.form_submissions (
                    installation_id, submission_id, form_id, round_id, status,
                    fields_json, outcome_json, score, reward_id, reward_title,
                    created_at_ms, updated_at_ms
                ) VALUES (
                    v_installation_id,
                    v_sub_id,
                    v_form_id,
                    COALESCE(v_payload ->> 'round_id', ''),
                    COALESCE(NULLIF(v_payload ->> 'status', ''), 'completed'),
                    '{}'::jsonb,
                    v_outcome,
                    v_score,
                    v_reward_id,
                    v_reward_title,
                    v_occurred_ms,
                    v_now_ms
                )
                ON CONFLICT (installation_id, submission_id) DO UPDATE SET
                    round_id      = CASE WHEN EXCLUDED.round_id <> '' THEN EXCLUDED.round_id ELSE public.form_submissions.round_id END,
                    status        = EXCLUDED.status,
                    outcome_json  = EXCLUDED.outcome_json,
                    score         = COALESCE(EXCLUDED.score, public.form_submissions.score),
                    reward_id     = COALESCE(EXCLUDED.reward_id, public.form_submissions.reward_id),
                    reward_title  = COALESCE(EXCLUDED.reward_title, public.form_submissions.reward_title),
                    updated_at_ms = GREATEST(public.form_submissions.updated_at_ms, EXCLUDED.updated_at_ms);
            END IF;

        -- C. Device Status / Pairing (monotonic LWW by origin_boot_ts_ms, origin_seq)
        ELSIF v_kind = 'device.status' OR v_kind = 'device.paired' THEN
            INSERT INTO public.devices (
                installation_id, device_id, public_key, role, device_name, device_type,
                manufacturer, model, os_name, os_version, app_version, framework_version,
                launcher_version, game_version, protocol_version,
                battery_pct, is_charging, temp_c, ssid, bssid, rssi_dbm, ip,
                ws_connected, game_running, origin_boot_ts_ms, origin_seq, updated_at_ms, raw_json
            ) VALUES (
                v_installation_id,
                COALESCE(NULLIF(v_payload ->> 'device_id', ''), v_origin_id),
                COALESCE(v_payload ->> 'public_key', ''),
                COALESCE(NULLIF(v_payload ->> 'role', ''), v_origin_role),
                COALESCE(v_payload ->> 'device_name', ''),
                COALESCE(v_payload ->> 'device_type', ''),
                COALESCE(v_payload ->> 'manufacturer', ''),
                COALESCE(v_payload ->> 'model', ''),
                COALESCE(v_payload ->> 'os_name', ''),
                COALESCE(v_payload ->> 'os_version', ''),
                COALESCE(v_payload ->> 'app_version', ''),
                COALESCE(v_payload ->> 'framework_version', ''),
                COALESCE(v_payload ->> 'launcher_version', ''),
                COALESCE(v_payload ->> 'game_version', ''),
                COALESCE((v_payload ->> 'protocol_version')::integer, 1),
                CASE WHEN jsonb_typeof(v_payload -> 'battery_pct') = 'number' THEN (v_payload ->> 'battery_pct')::double precision ELSE NULL END,
                CASE WHEN jsonb_typeof(v_payload -> 'is_charging') = 'boolean' THEN (v_payload ->> 'is_charging')::boolean ELSE NULL END,
                CASE WHEN jsonb_typeof(v_payload -> 'temp_c') = 'number' THEN (v_payload ->> 'temp_c')::double precision ELSE NULL END,
                v_payload ->> 'ssid',
                v_payload ->> 'bssid',
                CASE WHEN jsonb_typeof(v_payload -> 'rssi_dbm') = 'number' THEN (v_payload ->> 'rssi_dbm')::integer ELSE NULL END,
                v_payload ->> 'ip',
                CASE WHEN jsonb_typeof(v_payload -> 'ws_connected') = 'boolean' THEN (v_payload ->> 'ws_connected')::boolean ELSE NULL END,
                CASE WHEN jsonb_typeof(v_payload -> 'game_running') = 'boolean' THEN (v_payload ->> 'game_running')::boolean ELSE NULL END,
                COALESCE((v_payload ->> 'origin_boot_ts_ms')::bigint, v_occurred_ms),
                v_origin_seq,
                v_now_ms,
                v_payload
            )
            ON CONFLICT (installation_id, device_id) DO UPDATE SET
                public_key        = CASE WHEN public.devices.public_key = '' AND EXCLUDED.public_key <> '' THEN EXCLUDED.public_key ELSE public.devices.public_key END,
                device_name       = CASE WHEN EXCLUDED.device_name <> '' THEN EXCLUDED.device_name ELSE public.devices.device_name END,
                device_type       = CASE WHEN EXCLUDED.device_type <> '' THEN EXCLUDED.device_type ELSE public.devices.device_type END,
                manufacturer      = CASE WHEN EXCLUDED.manufacturer <> '' THEN EXCLUDED.manufacturer ELSE public.devices.manufacturer END,
                model             = CASE WHEN EXCLUDED.model <> '' THEN EXCLUDED.model ELSE public.devices.model END,
                os_name           = CASE WHEN EXCLUDED.os_name <> '' THEN EXCLUDED.os_name ELSE public.devices.os_name END,
                os_version        = CASE WHEN EXCLUDED.os_version <> '' THEN EXCLUDED.os_version ELSE public.devices.os_version END,
                app_version       = CASE WHEN EXCLUDED.app_version <> '' THEN EXCLUDED.app_version ELSE public.devices.app_version END,
                framework_version = CASE WHEN EXCLUDED.framework_version <> '' THEN EXCLUDED.framework_version ELSE public.devices.framework_version END,
                battery_pct       = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.battery_pct, public.devices.battery_pct) ELSE public.devices.battery_pct END,
                is_charging       = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.is_charging, public.devices.is_charging) ELSE public.devices.is_charging END,
                temp_c            = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.temp_c, public.devices.temp_c) ELSE public.devices.temp_c END,
                ssid              = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.ssid, public.devices.ssid) ELSE public.devices.ssid END,
                bssid             = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.bssid, public.devices.bssid) ELSE public.devices.bssid END,
                rssi_dbm          = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.rssi_dbm, public.devices.rssi_dbm) ELSE public.devices.rssi_dbm END,
                ip                = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.ip, public.devices.ip) ELSE public.devices.ip END,
                ws_connected      = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.ws_connected, public.devices.ws_connected) ELSE public.devices.ws_connected END,
                game_running      = CASE WHEN (EXCLUDED.origin_boot_ts_ms, EXCLUDED.origin_seq) >= (public.devices.origin_boot_ts_ms, public.devices.origin_seq) THEN COALESCE(EXCLUDED.game_running, public.devices.game_running) ELSE public.devices.game_running END,
                origin_boot_ts_ms = GREATEST(public.devices.origin_boot_ts_ms, EXCLUDED.origin_boot_ts_ms),
                origin_seq        = CASE WHEN EXCLUDED.origin_boot_ts_ms > public.devices.origin_boot_ts_ms THEN EXCLUDED.origin_seq WHEN EXCLUDED.origin_boot_ts_ms = public.devices.origin_boot_ts_ms THEN GREATEST(public.devices.origin_seq, EXCLUDED.origin_seq) ELSE public.devices.origin_seq END,
                updated_at_ms     = GREATEST(public.devices.updated_at_ms, EXCLUDED.updated_at_ms);

        -- D. Board / Leaderboard 2P-Set CRDT (tombstone always wins)
        ELSIF v_kind = 'board.entry_added' THEN
            IF COALESCE(v_payload ->> 'board_id', '') <> '' AND COALESCE(v_payload ->> 'entry_id', '') <> '' THEN
                INSERT INTO public.board_entries (
                    installation_id, board_id, entry_id, submission_id, sort_value,
                    display_json, created_at_ms, updated_at_ms
                ) VALUES (
                    v_installation_id,
                    v_payload ->> 'board_id',
                    v_payload ->> 'entry_id',
                    COALESCE(v_payload ->> 'submission_id', ''),
                    COALESCE((v_payload ->> 'sort_value')::double precision, 0),
                    COALESCE(v_payload -> 'display', '{}'::jsonb),
                    v_occurred_ms,
                    v_now_ms
                )
                ON CONFLICT (installation_id, board_id, entry_id) DO UPDATE SET
                    submission_id = CASE WHEN public.board_entries.submission_id = '' THEN EXCLUDED.submission_id ELSE public.board_entries.submission_id END,
                    sort_value    = EXCLUDED.sort_value,
                    display_json  = CASE WHEN public.board_entries.display_json = '{}'::jsonb THEN EXCLUDED.display_json ELSE public.board_entries.display_json END,
                    updated_at_ms = GREATEST(public.board_entries.updated_at_ms, EXCLUDED.updated_at_ms);
            END IF;

        ELSIF v_kind = 'board.entry_removed' THEN
            IF COALESCE(v_payload ->> 'board_id', '') <> '' AND COALESCE(v_payload ->> 'entry_id', '') <> '' THEN
                INSERT INTO public.board_entries (
                    installation_id, board_id, entry_id, created_at_ms,
                    removed_at_ms, removed_by_operator_id, updated_at_ms
                ) VALUES (
                    v_installation_id,
                    v_payload ->> 'board_id',
                    v_payload ->> 'entry_id',
                    v_occurred_ms,
                    v_occurred_ms,
                    COALESCE(v_payload ->> 'removed_by_operator_id', 'operator'),
                    v_now_ms
                )
                ON CONFLICT (installation_id, board_id, entry_id) DO UPDATE SET
                    removed_at_ms          = COALESCE(public.board_entries.removed_at_ms, EXCLUDED.removed_at_ms),
                    removed_by_operator_id = COALESCE(public.board_entries.removed_by_operator_id, EXCLUDED.removed_by_operator_id),
                    updated_at_ms          = GREATEST(public.board_entries.updated_at_ms, EXCLUDED.updated_at_ms);
            END IF;
        END IF;

        -- Always append immutable record into events (idempotent by event_id)
        INSERT INTO public.events (
            event_id, installation_id, origin_device_id, origin_role,
            origin_boot_id, origin_seq, category, kind, severity,
            occurred_at_ms, relayed_from, received_at_ms, payload_json
        ) VALUES (
            v_ev_id,
            v_installation_id,
            v_origin_id,
            v_origin_role,
            v_origin_boot,
            v_origin_seq,
            v_cat,
            v_kind,
            COALESCE(NULLIF(v_ev ->> 'severity', ''), 'info'),
            v_occurred_ms,
            NULLIF(v_ev ->> 'relayed_from', ''),
            v_now_ms,
            v_payload
        )
        ON CONFLICT (event_id) DO NOTHING;
    END LOOP;

    RETURN jsonb_build_object(
        'ok', true,
        'status', 200,
        'accepted_ids', to_jsonb(v_accepted_ids),
        'server_ms', v_now_ms
    );
END;
$$;
