# corekiosk

`corekiosk` is a toolkit, CLI, and library suite for building, running, and remotely updating games and brand-activation kiosk installations (Windows PC + Android/Web Companion Tablet + Cloud Telemetry & Auto-Updater).

---

## 1. Install `corekiosk` CLI First

Install the `corekiosk` CLI globally (or register it as a Go tool inside your project's `server/` module):

```powershell
# Install globally into $GOPATH/bin
go install github.com/GecEnterprises/corekiosk/go/cmd/corekiosk@latest

# Or pin as a tool inside your activation project's ./server module
go -C ./server get -tool github.com/GecEnterprises/corekiosk/go/cmd/corekiosk@latest
```

### Quick CLI Commands

```powershell
# 1. Interactive Supabase Wizard (scaffolds ./supabase, generates release.key, links & deploys telemetry + auto-updater)
corekiosk supabase

# 2. Local Development (live-reloads Go server, runs Companion Web/Metro, sets up adb reverse, launches Godot 4)
corekiosk dev

# 3. Android Studio, AVD & Standalone Dev APK (no Expo CLI required once prebuilt)
corekiosk android prebuild -studio   # Generate ./companion-rn/android and open in Android Studio
corekiosk android dev-apk            # Build self-contained debug APK via Gradle and install/launch on AVD
corekiosk android apk                # Build release APK into ./dist/

# 4. Package Portable Windows Release (.zip + kiosk.manifest.json)
corekiosk pack

# 5. Sign, Upload & Publish Release to Supabase Auto-Updater (reads .ckpublish.env & kiosk.json automatically)
corekiosk publish
```

---

## 2. Interactive Supabase & `ckupdater` Wizard (`corekiosk supabase`)

Running `corekiosk supabase` launches an interactive terminal wizard (`charmbracelet/huh`) that preps your entire cloud telemetry and auto-update infrastructure in one step:

1. **Prompts you interactively** for:
   - Which components to enable (**Telemetry & Leads Backend** and/or **Auto-Updater & Release Signing**).
   - Your **Supabase Project Reference ID** (e.g. `fdicweebtgnheqrekahr`).
   - Your shared **`CK_TELEMETRY_API_KEY`** and **`CK_PUBLISH_API_KEY`** (pre-filled with secure random secrets).
   - Your **Ed25519 Release Signing Keypair** path (defaults to `./release.key`; generated automatically if missing).
   - Your optional **`SUPABASE_SERVICE_ROLE_KEY`** (used to auto-create the private `ck-releases` Storage bucket and saved to gitignored `.ckpublish.env` for zero-flag `corekiosk publish`).
2. **Scaffolds `./supabase/`** directly from embedded migrations and Edge Functions (`config.toml`, `migrations/*.sql`, `functions/telemetry`, `functions/updates`).
3. **Updates `kiosk.json`** with `telemetry.url`, `updates.url`, `updates.channel`, and `updates.trusted_keys` (embedded into `kiosk.manifest.json` on `corekiosk pack` and automatically loaded by `cklauncher`).
4. **Invokes the Supabase CLI** (`supabase` or `npx supabase`) to:
   - `supabase link --project-ref <ref>`
   - `supabase db push`
   - `supabase secrets set CK_TELEMETRY_API_KEY=... CK_PUBLISH_API_KEY=... CK_RELEASES_BUCKET=...`
   - `supabase functions deploy telemetry --no-verify-jwt`
   - `supabase functions deploy updates --no-verify-jwt`

---

## 3. Core Libraries & Architecture

### `ckframework`
Go library where you define the kiosk/game loop, phases, forms, typed settings, admin actions/triggers, and server- or game-defined leaderboards/boards. Forms default to `CKTelemetryTransmit`, meaning every form submission is saved atomically into both the immutable local submissions ledger and the telemetry outbox.

`ckframework` is used downstream by `ckserver` and `cklauncher`, and generates the matching TypeScript and Godot version/type contracts for clients and game bridges.

### `ckserver`
Embeddable Go server library built on Gin, `coder/websocket`, SQLite (WAL), `goqu` (query builder), `goose` (embedded migrations), and `errorx`.

Provides fixed endpoints for persistent Ed25519 key pairing (zero-PIN IP-only reconnect once paired), rich device metadata tracking, Operator login guarded by Argon2id + EdDSA JWTs (defaults to `operator` / `operator`, editable in `cklauncher`), real-time settings and action mirroring to the game over loopback `/v1/bridge`, immutable form history (read/export only), delete-only leaderboard/board moderation, and bidirectional LAN telemetry relay.

### `cklauncher` & `ckupdater`
Windows supervisor, native Fyne v2 GUI (`fyne.io/fyne/v2`), in-process Ed25519-verified auto-updater (`ckupdater`), and monorepo packager (`ckpack`).

`cklauncher` starts the API server (`:8080`) and optional bundled Companion Web host (`:8090`) before spawning the sidecared game binary with `--server-url` and `--bridge-token`, runs a configurable pre-launch countdown, auto-restarts the game on crash, installs itself via Windows Task Scheduler (`--install-autostart`), and polls the configured `updates` endpoint to stage, verify, and atomically swap new releases (`when_idle`, `next_restart`, or `immediate`) with `/v1/health` rollback protection.

### `cktelemetry`
Platform-agnostic, local-first telemetry outbox (`goqu` + `goose`) with Ed25519-signed gzipped HTTPS batch uploading to any cloud ingest endpoint (such as the embedded `supabase/` PostgreSQL + Edge Function backend or the standalone `cktelemetry-server`).

Supports live configuration and connection testing in `cklauncher`, bidirectional LAN relay (including Tablet-as-Mule sync when the kiosk PC has no internet), conflict-free deterministic IDs, out-of-order `form.submission` + `form.outcome` merging, and Windows kernel-event Wi-Fi/adapter tracking and host vitals collection.

### `@corekiosk/client`, `@corekiosk/rn` & `@corekiosk/telemetry-rn`
- `@corekiosk/client` (`js/packages/client`): Headless TypeScript SDK with persistent Ed25519 crypto (works across Web, Node, and React Native Hermes), mutual challenge-response auth, REST API client, auto-reconnecting WebSocket with clock-offset sync and operator elevation, dynamic form validation, and reactive store.
- `@corekiosk/rn` (`js/packages/rn`): React Native / Expo & React Native Web UI toolkit (`<KioskProvider>`, `<KioskPhaseRouter>`, `ConnectScreen`, `OperatorLoginModal`, `OperatorScreen`, `DynamicForm`, `ExpoSecureStorage`, `WebLocalStorage`).
- `@corekiosk/telemetry-rn` (`js/packages/telemetry-rn`): Local-first SQLite outbox, Wi-Fi/battery collectors, and platform-agnostic Tablet-as-Mule cloud telemetry sync.

### `adapters`
Thin loopback `/v1/bridge` clients for game engines:
- Godot 4 (`adapters/godot/ck_bridge.gd`)
- Web / HTML5 / Electron (`adapters/web/ck-bridge.ts`)

### Docs
- [LAYOUT.md](./LAYOUT.md) — architecture, directory layout, auth layers, and wire protocol
- [TELEMETRY.md](./TELEMETRY.md) — local-first outbox, peer relay, and platform-agnostic cloud sync specification
- [VERSIONING.md](./VERSIONING.md) — 4-track versioning (`ckframework`, `cklauncher`, `client`, `game`) and compatibility handshake
- [supabase/README.md](./supabase/README.md) — Supabase manual & CLI deployment reference

### License
BSD 3-Clause License — see [LICENSE](./LICENSE).
