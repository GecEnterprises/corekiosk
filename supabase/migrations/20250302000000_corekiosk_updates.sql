-- =============================================================================
-- corekiosk — Agnostic Auto-Updater & Release Signing Registry (Supabase)
-- =============================================================================

CREATE TABLE IF NOT EXISTS public.release_keys (
    key_id        TEXT PRIMARY KEY,
    app_name      TEXT NOT NULL DEFAULT '',
    public_key    TEXT NOT NULL,
    label         TEXT NOT NULL DEFAULT '',
    revoked_at_ms BIGINT,
    created_at_ms BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS public.releases (
    app_name        TEXT NOT NULL,
    channel         TEXT NOT NULL DEFAULT 'stable',
    target          TEXT NOT NULL, -- win64 | game | companion_web | android
    version         TEXT NOT NULL,
    policy          TEXT NOT NULL DEFAULT 'when_idle', -- immediate | when_idle | next_restart
    storage_path    TEXT NOT NULL DEFAULT '',
    artifact_url    TEXT NOT NULL DEFAULT '',
    sha256          TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL DEFAULT 0,
    key_id          TEXT NOT NULL,
    sig             TEXT NOT NULL,
    notes           TEXT NOT NULL DEFAULT '',
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    published_at_ms BIGINT NOT NULL,
    PRIMARY KEY (app_name, channel, target, version)
);

CREATE INDEX IF NOT EXISTS idx_releases_latest
    ON public.releases (app_name, channel, target, is_active, published_at_ms DESC);

CREATE TABLE IF NOT EXISTS public.installation_channels (
    installation_id TEXT PRIMARY KEY,
    app_name        TEXT NOT NULL DEFAULT '',
    channel         TEXT NOT NULL DEFAULT 'stable',
    pinned_version  TEXT NOT NULL DEFAULT '',
    updated_at_ms   BIGINT NOT NULL
);
