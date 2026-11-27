# corekiosk

corekiosk is a toolkit and set of libraries for deploying games and brand activation installations in a kiosk-manner.

## ckframework
ckframework is a Go library where you define the kiosk/game loop, phases, forms, typed settings, admin actions/triggers, and server- or game-defined leaderboards/boards. Forms default to `CKTelemetryTransmit`, meaning every form submission is saved atomically into both the immutable local submissions ledger and the telemetry outbox.

ckframework is used downstream by ckserver and cklauncher, and generates the matching TypeScript and Godot version/type contracts for clients and game bridges.

## ckserver
ckserver is the embeddable Go server library built on Gin, coder/websocket, SQLite (WAL), goqu (query builder), goose (embedded migrations), and errorx.

It provides fixed endpoints for persistent Ed25519 key pairing (zero-PIN IP-only reconnect once paired), rich device metadata tracking, Operator login guarded by Argon2id + EdDSA JWTs (defaults to `operator` / `operator`, editable in cklauncher), real-time settings and action mirroring to the game over loopback `/v1/bridge`, immutable form history (read/export only), delete-only leaderboard/board moderation, and bidirectional LAN telemetry relay.

## corekiosk cli (`dev` & `pack`)
`github.com/GecEnterprises/corekiosk/go/cmd/corekiosk` provides the unified developer and release CLI (add via `go get -tool github.com/GecEnterprises/corekiosk/go/cmd/corekiosk`):
- `corekiosk dev`: reads `kiosk.json`, builds and runs `./server` with live `.go` file watching/auto-restart, launches Expo React Native Web (`./companion-rn`), and starts Godot (`./game`) with a shared loopback bridge token and clean process-tree teardown.
- `corekiosk pack`: runs typechecks/tests, exports Companion Web, exports the Godot 4 project, builds the Fyne `windowsgui` server binary, writes `kiosk.manifest.json`, and creates a portable release `.zip`.

## cklauncher
A Windows supervisor, native GUI, and monorepo packager (`ckpack`) for corekiosk apps, written in Go with https://fyne.io (`fyne.io/fyne/v2`).

cklauncher is used as a library called from your server binary rather than an external wrapper. It holds a direct reference to ckserver, starts the API server (`:8080`) and optional bundled Companion Web host (`:8090`) before spawning the sidecared game binary with `--server-url` and `--bridge-token`, runs a 30-second pre-launch countdown, auto-restarts the game on crash unless cancelled, and installs itself via Windows Task Scheduler (`--install-autostart`).

Its native desktop UI includes live Wi-Fi & self-healing listener controls, one-click Companion Web launch, live settings and admin triggers, booth operator credential management, paired device key revocation, board moderation, form CSV export, and an offline Telemetry Explorer.

`ckpack build` reads a root `kiosk.json` (or auto-detects `./server`, `./game`, and `./companion-rn`), builds the React Native Web bundle, exports the Godot 4 project, compiles the Go server/launcher binary, writes `kiosk.manifest.json`, and produces a portable release `.zip`.

## cktelemetry
[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/GecEnterprises/corekiosk/tree/main/cloudflare/telemetry-worker)

Local-first telemetry outbox (`goqu` + `goose`) with Ed25519-signed gzipped batch uploading to:
- **Supabase (`supabase/`)**: Paste-and-run PostgreSQL schema + views (`player_leads_view`, `prize_tallies_view`), single-roundtrip atomic RPC (`ck_ingest_batch`), and zero-dependency single-file Edge Function (`supabase/functions/telemetry/index.ts`) deployable 100% in the Supabase browser dashboard.
- **Cloudflare Worker + D1 (`cloudflare/telemetry-worker/`)**: Edge SQLite + optional R2 `.db.gz` backups + built-in `/admin` console.
- **Standalone (`go/cktelemetry/cmd/cktelemetry-server`)**: Self-hosted Go ingest binary.

Supports bidirectional LAN relay (including Tablet-as-Mule sync when the kiosk PC has no internet), conflict-free deterministic IDs, relational D1 form & prize-redemption tables (`form_submissions` with out-of-order `form.outcome` merging), built-in `/admin` reporting & RFC-4180 CSV export, and Windows kernel-event Wi-Fi/adapter tracking and host vitals collection.

## ckclient, rn & telemetry-rn
- `@corekiosk/client` (`js/packages/client`): Headless TypeScript SDK with persistent Ed25519 crypto, mutual challenge-response auth, REST API client, auto-reconnecting WebSocket with clock-offset sync and operator elevation, dynamic form validation, and reactive store.
- `@corekiosk/rn` (`js/packages/rn`): React Native / Expo & React Native Web UI toolkit (`<KioskProvider>`, `<KioskPhaseRouter>`, `ConnectScreen`, `OperatorLoginModal`, `OperatorScreen`, `DynamicForm`, `ExpoSecureStorage`, `WebLocalStorage`).
- `@corekiosk/telemetry-rn` (`js/packages/telemetry-rn`): Local-first SQLite outbox, Wi-Fi/battery collectors, and Tablet-as-Mule cloud telemetry sync.

## adapters
Thin loopback `/v1/bridge` clients for game engines:
- Godot 4 (`adapters/godot/ck_bridge.gd`)
- Web / HTML5 / Electron (`adapters/web/ck-bridge.ts`)

## docs
- [LAYOUT.md](./LAYOUT.md) — architecture, directory layout, auth layers, and wire protocol
- [TELEMETRY.md](./TELEMETRY.md) — local-first outbox, peer relay, and Cloudflare Worker + D1 sync design
- [VERSIONING.md](./VERSIONING.md) — 4-track versioning (`ckframework`, `cklauncher`, `client`, `game`) and compatibility handshake

## license
BSD 3-Clause License — see [LICENSE](./LICENSE).
