# CoreKiosk LAN discovery & auto-connect

Status: implemented in corekiosk v0.3.0 (2026-11-27)  
Scope: `ckframework`, `ckserver`, `cklauncher`, `ckpack`, `@corekiosk/client`,
`@corekiosk/rn`, and `@corekiosk/discovery-rn` (Android first, Web no-op, iOS later)

## 1. Goal

A companion tablet should find its kiosk on the venue LAN without an operator typing an IP,
and re-find it automatically when DHCP changes the host address.

Requirements:

- Zero-PIN reconnect for an already-paired tablet, even if the kiosk IP changed.
- First-time pairing still requires the 6-digit code shown on the kiosk. Discovery must
  never bypass pairing or trust an unauthenticated announcement.
- Work with multiple kiosk servers on the same LAN, and with multiple NICs on the host.
- Keep manual IP + code entry as a fallback that always works.
- Do not add a hard dependency on multicast working: venue Wi-Fi often has client/AP
  isolation, and Windows Firewall blocks inbound discovery by default.

Non-goals:

- NAT traversal / connecting over the internet (the existing cloud relay covers that).
- Replacing the pinned Ed25519 identity model.
- Exposing operator or player data through discovery.
- Wi-Fi Direct / Bluetooth / NFC onboarding (possible later; not needed for v1).

## 2. Recommended stack

| Method | Direction | Why | Default |
| --- | --- | --- | --- |
| **UDP broadcast probe/offer** | client ↔ server | Simplest; no multicast lock; works on Windows and Android; low latency; easy to debug | **on** |
| **mDNS / DNS-SD** (`_corekiosk._tcp`) | server advertises, client browses | Android `NsdManager` is built in; human-readable names; no multicast lock; survives some networks that drop plain broadcast | **on** (phase 2) |
| **SSDP** (`urn:gec:corekiosk:server:1`) | server advertises, client M-SEARCH | Extra compatibility with network gear / UPnP-aware environments; standard-ish | **off by default**, enable per deployment (phase 3) |
| **QR / manual IP** | operator | Last-resort fallback; works even with AP isolation | **manual already; QR optional** |

Rationale for UDP first: it is the only method that needs no permission, no multicast lock,
and no mDNS responder, and it exercises the same "candidate host + health check" path as the
other methods. mDNS is the best Android-native upgrade. SSDP is optional and should not be
shipped on by default because it is noisier and has more device-specific quirks.

> **Important deployment reality:** if the venue AP has client isolation enabled, no
> discovery protocol can cross it. The app must say so clearly and fall back to manual entry
> or a QR code. A dedicated travel router / kiosk-hosted hotspot is the robust fix, not more
> protocols.

## 3. Wire protocols

All discovery traffic is **out-of-band**; it does not change the `/v1` protocol version.
The client only uses an offer to obtain a candidate `host:port`, then always calls
`GET /v1/health` and verifies `server_id` / `server_pub` before authenticating.

### 3.1 UDP probe/offer (primary)

Port **`8099/udp`** (fixed; chosen to avoid 8080 API, 8090 Companion Web, 8081 Metro,
1900 SSDP, 5353 mDNS). JSON, UTF-8, one datagram per message, max 1 KiB.

Probe (client → `255.255.255.255:8099` and each interface's broadcast address):

```json
{"ck":1,"t":"probe","n":"9f2c1a7b4e6d8031","p":1}
```

Offer (server → unicast to the probe source):

```json
{
  "ck": 1,
  "t": "offer",
  "n": "9f2c1a7b4e6d8031",
  "id": "<server_id>",
  "name": "Joyland Arcade",
  "app": "joyland-essential",
  "port": 8080,
  "proto": 1,
  "v": "0.3.0",
  "caps": ["udp", "mdns", "ssdp"]
}
```

Rules:

- Echo the probe nonce; ignore offers whose nonce does not match.
- Do not include `server_pub`, the pairing code, telemetry, or operator data. The client
  fetches `/v1/health` for identity.
- Tolerate unknown fields (forward compatibility). Ignore offers with `ck != 1` or
  `proto < 1`.
- Server responds only to datagrams whose source is private/link-local; rate-limit to
  ~5 probes/second/source IP; one response per probe.
- Server binds `0.0.0.0:8099`; replies via the same socket so the OS picks the correct
  source interface. It also accepts unicast probes on `127.0.0.1` for tests.
- If 8099 is already bound, disable only the UDP method and report it in discovery status;
  do not fail server startup.
- Client sends to the global broadcast and (on Android) to every `NetworkInterface`
  broadcast address. Broadcast reception generally does not need a multicast lock; if a
  device drops it, acquire a short-lived `MulticastLock` as a fallback.

### 3.2 mDNS / DNS-SD (phase 2)

Service type: **`_corekiosk._tcp.local.`**

- Instance: `<name> [<server_id prefix>]._corekiosk._tcp.local.` (sanitize to <= 63 bytes,
  escape dots/backslashes; append the ID only when needed to avoid collisions).
- SRV: port = HTTP API port; target = `<hostname>.local.`
- TXT:
  ```
  id=<server_id>
  app=<app_name>
  proto=1
  path=/v1/health
  v=<framework_version>
  caps=udp,ssdp
  ```
- A/AAAA records for every active LAN address (IPv4 first, IPv6 when available).
- TTL ~4500s; re-announce every ~15 min and on network/IP change; unregister on shutdown.

Server implementation: `github.com/grandcat/zeroconf` (pure Go, register + browse) is the
smallest fit. It handles interface changes and name conflicts. Keep it behind a build-time
dependency; no CGO.

Android implementation: `NsdManager` (`discoverServices` + queued `resolveService`).
`resolveService` is serialized by Android; queue found services and retry
`FAILURE_ALREADY_ACTIVE`. TXT attributes arrive as `Map<String, ByteArray>`.

### 3.3 SSDP (phase 3, optional)

- Multicast group `239.255.255.250:1900`.
- Advertise `NT: urn:gec:corekiosk:server:1`.
- `USN: urn:gec:corekiosk:<server_id>` (URN form; no need to invent a UUID).
- `LOCATION: http://<lan-ip>:<http-port>/v1/health`.
- `CACHE-CONTROL: max-age=1800`, `SERVER: corekiosk/<version> UPnP/1.1`,
  `X-CK-ID: <server_id>`, `X-CK-NAME: <name>`.
- Respond to `M-SEARCH` with `ST` matching our URN (and optionally `ssdp:all`), after a
  random 0-100 ms delay to avoid response storms.
- Send `ssdp:alive` NOTIFY on startup and every ~15 min; `ssdp:byebye` on shutdown.
- Multi-NIC: join/send on every active interface (`golang.org/x/net/ipv4` already available).
- Android client: active `M-SEARCH` from an ephemeral `DatagramSocket` and collect unicast
  responses. This usually needs no `MulticastLock`; add the lock only if field devices drop
  multicast egress. Passive `NOTIFY` listening (join group 1900) is optional and requires
  `CHANGE_WIFI_MULTICAST_STATE` + `MulticastLock`.

### 3.4 QR / manual fallback

The kiosk launcher already displays `ip:port` and the pair code. Optional phase 4:

- Show a QR on the kiosk display/launcher containing
  `corekiosk://pair?host=<ip>&port=<port>&id=<server_id>` (or a plain
  `http://<ip>:<port>/v1/health` URL).
- Scan with `expo-camera`; fill the address and still require the 6-digit code.
- Keep the manual field visible at all times.

## 4. Server changes (`corekiosk` Go)

### 4.1 New package `go/ckdiscovery`

```go
package ckdiscovery

type Config struct {
    Enabled     bool
    Name        string          // "Joyland Arcade"; defaults to spec app name
    Methods     []string        // "udp", "mdns", "ssdp"
    UDPPort     int             // default 8099
    HTTPPort    int             // filled from the live listener
    AdvertiseEvery time.Duration // default 15m
}

type Info struct {
    ServerID        string
    ServerPub       string
    AppName         string
    Name            string
    HTTPPort        int
    ProtocolVersion int
    Version         string
    Addresses       []string // live LAN IPs, primary first
}

type Advertiser struct{ /* ... */ }

func New(cfg Config) *Advertiser
func (a *Advertiser) Start(ctx context.Context, info Info) error
func (a *Advertiser) Update(info Info) // listener/IP/port change
func (a *Advertiser) Status() Status
func (a *Advertiser) Close() error
```

Files: `advertiser.go`, `protocol.go`, `udp.go`, `ssdp.go`, `mdns.go`, `status.go`, tests.

Behavior:

- Start each enabled method independently. A failure in mDNS or SSDP logs a warning and
  leaves the others running; `Status()` reports `{udp: true, mdns: false, error: "..."}`.
- `Update` is cheap and idempotent: refresh addresses/port, re-announce, re-bind if the
  listener port changed.
- Never return an error from `Start` for a single transport failure; only a configuration
  error (e.g. no methods) is fatal.
- No pairing code, operator info, or telemetry in any discovery payload.

### 4.2 Wire into `ckserver`

- `Server` gains `discovery *ckdiscovery.Advertiser` and `discoveryCfg`.
- New option `WithDiscovery(ckdiscovery.Config)` and/or
  `ConfigureDiscovery(cfg)` callable before `Start` (needed because some apps call
  `ckserver.New` then `cklauncher.Run`).
- In `Server.Start`, after `s.listener.start(...)` returns the bound address:
  1. Parse the live port.
  2. Build `Info` from `s.identity`, `s.spec`, `LocalIPv4s()`.
  3. `s.discovery.Start(ctx, info)`.
- In `listenerManager.watchNetworkLoop` (already detects IP changes) call
  `s.discovery.Update(info)` when `curIPs` changes or after `RestartListener`.
- In `Server.Close`, stop the advertiser (`byebye` + mDNS unregister).
- Add `Server.DiscoveryStatus()` for the launcher UI and actions.

### 4.3 Wire into `cklauncher`

- `Options` gains `Discovery *ckdiscovery.Config` (or flat fields).
- Before `srv.Start(...)` call `srv.ConfigureDiscovery(...)` with:
  - `Name` from `discovery.name` in the manifest, else `spec.Name`.
  - `Methods` from config, default `["udp","mdns"]`.
  - `Enabled` default true for kiosk launcher; `--no-discovery` / `CK_DISCOVERY=0`
    disables it.
- GUI "Pairing & Network" panel gains a `Discovery` row:
  - `UDP 8099 · mDNS · SSDP` + green/red state per method.
  - A hint when Windows Firewall may block it, with the exact rule to add.
- Log `discovery.started` at startup and a telemetry event on transport errors
  (rate-limited).
- Optional: `corekiosk discover` CLI command that runs the Go browser and prints offers;
  invaluable for field debugging.

### 4.4 Windows Firewall

Discovery needs inbound rules:

- UDP 8099 (UDP probe)
- UDP 5353 (mDNS, if enabled)
- UDP 1900 (SSDP, if enabled)
- TCP 8080 (existing API)

Do **not** silently elevate. Recommended:

1. Ship `scripts/windows-firewall.ps1` with the app (Joyland) that adds rules for the
   current exe on the Private profile.
2. In the launcher, show a non-blocking "Discovery may be blocked by Windows Firewall"
   banner if a self-probe fails, with the command to run.
3. Optionally, `ckpack` can emit a small `install-firewall.ps1` into the release zip.

## 5. Companion changes (TypeScript/React Native)

### 5.1 New native package `@corekiosk/discovery-rn`

An Expo module (Kotlin first, Swift later), published next to `@corekiosk/rn`.
Keeping it separate means web/desktop builds do not pull native code and apps opt in.

Android implementation:

- **UDP**: coroutine scan; `DatagramSocket` with `broadcast = true`; send probes to
  `255.255.255.255` and every `NetworkInterface` broadcast address; collect offers until
  `timeoutMs`; merge by `server_id`.
- **mDNS**: `NsdManager` discover + serialized resolve queue; emit resolved host/port/TXT.
- **SSDP** (optional): ephemeral `DatagramSocket`, send `M-SEARCH`, parse unicast
  responses; `MulticastLock` only if needed.
- Emit a merged list whenever a new offer arrives; dedupe by `serverId` and merge hosts and
  sources.
- Scan only while foreground; `stopScan()` in `onPause`/unmount; release locks in `finally`.
- Permissions (via the `@corekiosk/rn` Expo config plugin):
  - `android.permission.CHANGE_WIFI_MULTICAST_STATE` (normal; only needed for SSDP lock)
  - `android.permission.ACCESS_NETWORK_STATE` (already)
  - `android.permission.ACCESS_WIFI_STATE` (already)
  - `NEARBY_WIFI_DEVICES` with `usesPermissionFlags="neverForLocation"` **only if** the
    target Android version requires it for `NsdManager`; verify on the SM-X510 first.

TypeScript surface:

```ts
export type DiscoverySource = "udp" | "mdns" | "ssdp";

export interface DiscoveredServer {
  serverId: string;
  name: string;
  host: string;        // best host
  hosts: string[];     // all addresses seen for this server
  port: number;
  sources: DiscoverySource[];
  appName?: string;
  version?: string;
  protocolVersion?: number;
  lastSeenMs: number;
}

export interface DiscoveryAdapter {
  startScan(opts?: { timeoutMs?: number; methods?: DiscoverySource[] }): Promise<void>;
  stopScan(): Promise<void>;
  subscribe(listener: (servers: DiscoveredServer[]) => void): () => void;
}
```

Pure merge/rank helpers live in `@corekiosk/client` (`discovery.ts`) so they are testable
without native code:

```ts
export function mergeOffers(a: DiscoveredServer[], b: DiscoveredServer[]): DiscoveredServer[];
export function rankServers(servers: DiscoveredServer[], pinnedId: string | null, localIp?: string): DiscoveredServer[];
```

Ranking: pinned `serverId` first, then same `/24` as the tablet, then mDNS > UDP > SSDP,
then lowest health RTT / most recent offer.

### 5.2 `KioskProvider`

New optional prop:

```ts
discovery?: DiscoveryAdapter;
discoveryTimeoutMs?: number;      // default 3500
discoveryFallbackMs?: number;     // default 2500 after socket start
autoReconnect?: boolean;          // default true
```

New context fields:

```ts
discovered: DiscoveredServer[];
discovering: boolean;
discoveryError: string | null;
pinnedServerIds: string[];
scanForServers(timeoutMs?: number): Promise<DiscoveredServer[]>;
connectToDiscovered(server: DiscoveredServer, pairCode?: string): Promise<void>;
```

Boot flow:

1. Load keypair, pins, `LAST_HOST_KEY`.
2. If a pinned host exists, start the socket immediately (fast path, as today).
3. If no pin, start a scan immediately (Connect screen is already visible).
4. If a pin exists but the socket is not connected after `discoveryFallbackMs`, start a
   scan in parallel. Do not block the UI.
5. On an offer whose `serverId` matches the pin:
   - Try each `host` with `GET /v1/health` (short timeout).
   - Require `health.server_id === pin.serverId` **and**
     `health.server_pub === pin.serverPub`.
   - If both match, call the existing `connectOrPair(host:port)` path (it already does
     `authenticateWithKey` and persists the new host), then persist `LAST_HOST_KEY` and
     `pinned.lastIp`.
   - If the ID matches but the pub key differs, mark it as an identity mismatch and refuse
     to connect. This is a spoofing attempt or a re-keyed server.
6. On an offer with no pin / different ID: add it to `discovered` only. Never auto-pair.
7. Re-scan triggers: app foreground (`AppState` back to `active`), Wi-Fi reconnect
   (`NetInfo`), socket disconnected for > N seconds, Connect screen opened, manual
   "Search again" button.

Guard auto-reconnect with a generation counter/mutex so two offers cannot start two sockets.
Use capped exponential backoff on failed auto-connects.

### 5.3 `ConnectScreen`

Add a "Nearby kiosks" block above the manual form:

- Shows a short spinner while scanning.
- Each row: name, `host:port`, source badges (`mDNS`, `UDP`), and a `Paired` chip when
  `serverId` is in `pinnedServerIds`.
- Tapping a paired row: zero-PIN reconnect (fill address, call connect).
- Tapping a new row: fill the address, leave the code field focused; the operator still
  enters the 6-digit code.
- Empty state: "No kiosks found — make sure the tablet and kiosk are on the same Wi-Fi;
  you can still enter the address shown on the kiosk."
- Manual IP/code fields stay visible and always work.
- Retry button re-runs a 3-4 s scan.

Web build: `DiscoveryAdapter` is a no-op; the screen keeps the existing
`location.hostname:8080` default.

### 5.4 App wiring (Joyland)

`companion-rn/src/platform.native.ts`:

```ts
import { createDiscoveryAdapter } from "@corekiosk/discovery-rn";
// ...
discovery: createDiscoveryAdapter({ methods: ["udp", "mdns"] }),
```

`companion-rn/src/platform.web.ts`: omit discovery (or pass a no-op adapter).
`App.tsx`: pass `env.discovery` to `<KioskProvider discovery={env.discovery} ... />`.
`app.json`: add the new Expo plugin/module (autolinked after `expo prebuild`).
`kiosk.json`: add the discovery block (see below).

## 6. Configuration

`kiosk.json`:

```json
"discovery": {
  "enabled": true,
  "name": "Joyland Arcade",
  "methods": ["udp", "mdns"],
  "udp_port": 8099,
  "advertise_interval_seconds": 900
}
```

Precedence (server): CLI flag > env (`CK_DISCOVERY`, `CK_DISCOVERY_NAME`,
`CK_DISCOVERY_METHODS`, `CK_DISCOVERY_UDP_PORT`) > `kiosk.manifest.json` > defaults.

`ckpack` should copy the discovery block into `kiosk.manifest.json`; `cklauncher` reads it
at runtime. The Joyland `server/main.go` needs no change if the launcher applies the
defaults.

Suggested defaults: enabled in `cklauncher`, methods `udp` + `mdns`, name = spec app name,
UDP port 8099, scan timeout 3500 ms, fallback delay 2500 ms.

## 7. Security & trust

- Discovery is unauthenticated and spoofable. Treat every offer as a hint, never as trust.
- Pinned reconnect requires both `server_id` and `server_pub` to match the stored pin;
  `authenticateWithKey` already enforces the pub-key match with a 403
  `server_key_mismatch`.
- First-time pairing always requires the 6-digit code; discovery only pre-fills the address.
- On a pinned-ID/pub mismatch, do not fall back to pairing; show an explicit error and
  keep the old pin.
- No secrets in discovery packets: no `server_pub`, no pairing code (except the existing
  dev-mode `/v1/health` field), no operator/player data.
- Rate-limit probes/responses; ignore non-private sources; cap packet sizes.
- Do not let a discovery offer trigger unbounded retries; use backoff and a per-session
  blacklist for mismatched servers.
- Optional future hardening: sign offers with the server key. Not needed for v1 because
  the health + pin check is the real trust anchor.

## 8. Failure modes

| Situation | Behavior |
| --- | --- |
| Paired, IP unchanged | Connect directly; no scan (fast path) |
| Paired, IP changed | Scan after 2.5 s; match `server_id` + `server_pub`; reconnect; update host |
| Paired, server offline | Scan; show "Kiosk not found" with Retry; manual entry still works |
| Two kiosks on LAN | Pinned one auto-reconnects; both listed in Connect screen |
| AP/client isolation | No offers; clear "not found" message; manual/QR fallback |
| Windows Firewall blocks UDP | mDNS may still work; if both blocked, show firewall hint |
| mDNS port 5353 occupied | UDP still works; status reports mDNS unavailable |
| UDP port 8099 occupied | mDNS still works; status reports UDP unavailable |
| Malicious offer with matching ID, wrong pub | Refuse, `discovery.identity_mismatch`, keep old pin |
| IPv6-only network | mDNS AAAA (phase 2); UDP/SSDP are IPv4-only; normalize bracketed IPv6 URLs |
| Android doze / background | Scan only in foreground; stop on pause; release locks |

## 9. Build/packaging prerequisite

The current release build has a correctness bug that will also bite discovery changes:
Gradle's `createBundleReleaseJsAndAssets` can be `UP-TO-DATE` after `node_modules` changes,
so the APK can ship a stale JS bundle. (Observed while fixing the 0.2.15 companion: the
first rebuild reused the old 0.2.11 bundle.)

Fix in `corekiosk android apk` / `dev-apk`:

- Before `assembleRelease` (and `assembleDebug` for dev-apk), delete
  `android/app/build/generated/assets/createBundleReleaseJsAndAssets` and the downstream
  release asset intermediates, **or** run
  `:app:createBundleReleaseJsAndAssets --rerun-tasks` when `package-lock.json`,
  `node_modules/@corekiosk/*`, or `app.json` changed.
- Simplest reliable version: always delete the generated bundle directory before a release
  build. Cost is ~10-20 s for Metro; the guarantee is worth it.
- Add a `-fresh-js` flag for an explicit full regeneration, and log when the bundle is
  regenerated.

This belongs in the same release as discovery so field builds cannot silently ship old
discovery code.

## 10. Test plan

### Go

- Protocol unit tests: probe/offer JSON validation, nonce echo, size caps, unknown fields.
- UDP responder: unicast probe to `127.0.0.1:8099` returns an offer; public-source packet
  ignored; rate limit per source.
- SSDP: M-SEARCH parse/response, NOTIFY alive/byebye, multi-interface sender (inject
  interfaces in tests).
- mDNS: TXT/SRV/A record registration and unregistration; name sanitization.
- `ckserver` integration: start server on `:0`, assert `DiscoveryStatus()` reports the
  bound port; simulate IP change and assert `Update` was called.
- `corekiosk discover` CLI smoke test against a live server.

### TypeScript / Android

- `mergeOffers` / `rankServers` unit tests (pinned first, dedupe, same-subnet preference).
- Provider auto-reconnect test with a fake `DiscoveryAdapter`:
  - pinned host fails → offer with same ID/pub → connect succeeds once.
  - offer with matching ID, wrong pub → refused, no reconnect loop.
  - two offers → only one socket/generation.
- Kotlin parser tests for offer JSON, SSDP headers, NsdManager TXT.
- Instrumented test on the actual SM-X510:
  - UDP broadcast scan, mDNS browse, SSDP M-SEARCH.
  - Foreground scan, background stop, lock release.

### Manual matrix

1. Fresh install, one kiosk: list appears; tap → code → pair.
2. Paired, same IP: instant reconnect, no scan flash.
3. Paired, DHCP IP change: relaunch → auto reconnect < 5 s.
4. Two kiosks: pinned auto-connects; both listed.
5. AP isolation: no results, manual entry works, clear message.
6. Windows Firewall on/off; Private vs Public profile.
7. Multi-NIC host (Wi-Fi + Ethernet): correct `host` chosen; no duplicate rows.
8. Tablet Wi-Fi sleep/resume; app foreground/background.
9. Server restart while app is open: reconnect after discovery.
10. IPv6-only LAN (phase 2): mDNS AAAA resolves and URL is bracketed.

Telemetry (rate-limited, no IPs): `discovery.scan`, `discovery.offer`, `discovery.timeout`,
`discovery.auto_reconnect_ok`, `discovery.identity_mismatch`.

## 11. Delivery phases

**Phase 0 — build fix + protocol skeleton (small)**
- Fix the stale JS bundle issue above.
- Add `ckdiscovery` config/status structs and UDP responder + unit tests.
- Add `corekiosk discover` CLI (optional but recommended).
- Exit: a probe from `nc`/PowerShell gets a valid offer from a live kiosk.

**Phase 1 — UDP autodiscovery end-to-end (MVP)**
- `ckserver` start/stop/update wiring; launcher config + status row.
- `@corekiosk/discovery-rn` UDP-only Expo module; provider state + auto-reconnect;
  Connect screen nearby list; Joyland wiring.
- Exit: fresh install lists the kiosk; paired tablet survives a DHCP address change
  without typing an IP; manual entry still works.

**Phase 2 — mDNS**
- Go mDNS advertiser; Android NsdManager browser; merge/rank by `server_id`.
- Exit: discovery works where UDP broadcast is filtered; no duplicate rows; scan < 2 s on
  the venue LAN.

**Phase 3 — SSDP (only if field data justifies it)**
- Go advertiser + Android M-SEARCH; status row; optional MulticastLock.
- Exit: SSDP finds the kiosk on at least one network family where UDP/mDNS failed.

**Phase 4 — polish**
- QR fallback, multi-kiosk selection UX, IPv6, iOS browser, firewall installer helper,
  discovery telemetry, operator diagnostics screen.

## 12. File map

Corekiosk:

```
go/ckdiscovery/                  # new: UDP/SSDP/mDNS advertiser, protocol, status, tests
go/ckserver/server.go            # discovery field, ConfigureDiscovery, Start/Close, status
go/ckserver/listener.go          # Update() on IP/port change
go/cklauncher/launcher.go        # config, start, status, --no-discovery
go/cklauncher/gui.go             # Discovery row in Pairing & Network
go/ckpack/pack.go                # copy discovery into manifest
go/cklauncher/manifest.go        # Discovery manifest struct
go/cmd/corekiosk/discover.go     # optional CLI browser
js/packages/client/src/discovery.ts   # types, merge/rank (pure)
js/packages/rn/src/provider.tsx       # discovery state, auto-reconnect
js/packages/rn/src/screens/ConnectScreen.tsx  # nearby kiosks UI
js/packages/rn/src/index.ts           # exports
js/packages/rn/app.plugin.js          # Android permissions
js/packages/discovery-rn/             # new Expo module (Android; iOS later)
  expo-module.config.json
  android/src/main/java/.../CorekioskDiscoveryModule.kt
  src/index.ts
  src/CorekioskDiscovery.types.ts
DISCOVERY.md                          # this document
README.md                             # docs index link
```

Joyland2:

```
kiosk.json                         # discovery block
companion-rn/src/platform.native.ts# createDiscoveryAdapter
companion-rn/src/platform.web.ts   # no-op / omitted
companion-rn/App.tsx               # pass discovery prop
companion-rn/app.json              # plugin/module entry if needed
server/main.go                     # no change expected (launcher owns discovery)
scripts/windows-firewall.ps1       # optional helper
```

## 13. Versioning

- Discovery is out-of-band; do **not** bump `ProtocolVersion` or `min_client_version`.
- Ship as a minor corekiosk release (e.g. `v0.3.0`) because it adds a package and a
  manifest/config field.
- Older companions ignore discovery and keep working via manual IP.
- New companions degrade gracefully if the native module is missing (catch import, hide the
  nearby list).
- If a `/v1/discovery` HTTP endpoint is ever added, it is additive and still does not
  change the protocol version. Recommendation for v1: **do not add it**; reuse
  `/v1/health`.

## 14. Decisions (locked 2026-11-27)

1. **UDP + mDNS are the default methods; SSDP is implemented and opt-in.** The server and
   companion both support SSDP, but `cklauncher` defaults to `udp,mdns`. Enable it with
   `discovery.methods: ["udp","mdns","ssdp"]` or `-discovery-methods udp,mdns,ssdp`.
2. **Native code lives in `@corekiosk/discovery-rn`.** `@corekiosk/rn` stays UI/JS-only;
   apps opt in by creating the adapter in their platform layer.
3. **Android tablet first.** The `DiscoveryAdapter` interface is cross-platform; iOS is
   still a later platform adapter.
4. **QR fallback remains later (phase 4).** Manual IP + code is the fallback for v0.3.0.
5. **Discovery is on by default in `cklauncher`**, disabled with `--no-discovery` or
   `CK_DISCOVERY=0`. Custom `ckserver` embedders opt in with `WithDiscovery`.
6. **Venue Wi-Fi isolation is a deployment concern, not a code fix.** Document it in the
   operator runbook: if AP/client isolation is on, use manual entry, a QR code (later), a
   dedicated AP with isolation off, or a kiosk-hosted hotspot.

### First release scope (v0.3.0)

Ship phases 0, 1, and 2 only:

- Phase 0: release-bundle freshness fix, `ckdiscovery` UDP responder, `corekiosk discover`.
- Phase 1: ckserver/launcher wiring, `discovery-rn` UDP adapter, provider auto-reconnect,
  Connect screen nearby list, Joyland wiring.
- Phase 2: mDNS advertiser + Android `NsdManager` browser + merge/rank.

Do **not** block v0.3.0 on SSDP, QR, iOS, or the firewall installer. Those follow in
phases 3 and 4.

### Suggested PR breakdown

1. **PR 1 — build + UDP server core.** Force fresh JS bundle in `corekiosk android apk`;
   add `go/ckdiscovery` UDP responder, protocol/status types, tests, and `corekiosk discover`.
2. **PR 2 — server integration.** `ckserver` configure/start/update/close; `cklauncher`
   config, GUI status row, `--no-discovery`; manifest/`kiosk.json` plumbing.
3. **PR 3 — Android UDP client.** New `@corekiosk/discovery-rn` Expo module (UDP first),
   `DiscoveryAdapter` + merge/rank in `@corekiosk/client`, provider state/auto-reconnect,
   Connect screen list; Joyland platform wiring.
4. **PR 4 — mDNS.** Go advertiser (`grandcat/zeroconf`), Android `NsdManager` browser,
   source merging, IPv6 AAAA handling.
5. **PR 5 — docs/field kit.** Firewall helper, operator runbook, discovery telemetry,
   diagnostics screen/CLI polish.
