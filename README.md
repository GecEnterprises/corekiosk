# corekiosk

corekiosk is a toolkit and set of libraries for deploying games and brand activation installations in a kiosk-manner.

## ckframework
ckframework is a Go library where you define the kiosk/game loop, phases, forms, typed settings, admin actions/triggers, and server- or game-defined leaderboards/boards. Forms default to `CKTelemetryTransmit`, meaning every form submission is saved atomically into both the immutable local submissions ledger and the telemetry outbox.

ckframework is used downstream by ckserver and cklauncher, and generates the matching TypeScript and Godot version/type contracts for clients and game bridges.

## ckserver
ckserver is the embeddable Go server library built on Gin, coder/websocket, SQLite (WAL), goqu (query builder), goose (embedded migrations), and errorx.

It provides fixed endpoints for persistent Ed25519 key pairing (zero-PIN IP-only reconnect once paired), rich device metadata tracking, Operator login guarded by Argon2id + EdDSA JWTs (defaults to `operator` / `operator`, editable in cklauncher), real-time settings and action mirroring to the game over loopback `/v1/bridge`, immutable form history (read/export only), delete-only leaderboard/board moderation, and bidirectional LAN telemetry relay.

## cklauncher
A Windows supervisor, native GUI, and packager (`ckpack`) for corekiosk apps, written in pure Go (`CGO_ENABLED=0`) with https://github.com/golang-gui/goui.

cklauncher is used as a library called from your server binary rather than an external wrapper. It holds a direct reference to ckserver, starts the server before spawning the sidecared game binary with `--server-url` and `--bridge-token`, runs a 30-second pre-launch countdown, auto-restarts the game on crash unless cancelled, and installs itself via Windows Task Scheduler (`--install-autostart`).

Its native desktop UI includes live settings and admin triggers, booth operator credential management, paired device key revocation, board moderation, form CSV export, and an offline Telemetry Explorer filterable by server/client origin, paired client key, category, and cloud sync status.

## cktelemetry
Local-first telemetry outbox (`goqu` + `goose`) with Ed25519-signed gzipped batch uploading to Firebase (via the included `firebase/functions` ingest function) or the standalone `cktelemetry-server`.

Supports bidirectional LAN relay (including Tablet-as-Mule sync when the kiosk PC has no internet), conflict-free deterministic IDs, table-shaped Firestore form collections (`forms/{formId}/rows/{submissionId}`), and Windows Wi-Fi disconnect/roaming and host vitals collection.

## ckclient
Headless TypeScript SDK (`js/packages/client`, `@corekiosk/client`) with zero UI dependencies: persistent Ed25519 crypto, mutual challenge-response auth, REST API client, auto-reconnecting WebSocket with clock-offset sync and operator elevation, dynamic form validation, and reactive store.

## ckclient-rn & cktelemetry-rn
Planned React Native / Expo UI toolkit and mobile Wi-Fi/battery/mule-sync collectors built on top of `@corekiosk/client`.

## adapters
Thin loopback `/v1/bridge` clients for game engines:
- Godot 4 (`adapters/godot/ck_bridge.gd`)
- Web / HTML5 / Electron (`adapters/web/ck-bridge.ts`)

## docs
- [LAYOUT.md](./LAYOUT.md) — architecture, directory layout, auth layers, and wire protocol
- [TELEMETRY.md](./TELEMETRY.md) — local-first outbox, peer relay, and Firebase sync design
- [VERSIONING.md](./VERSIONING.md) — 4-track versioning (`ckframework`, `cklauncher`, `client`, `game`) and compatibility handshake

## license
BSD 3-Clause License — see [LICENSE](./LICENSE).
