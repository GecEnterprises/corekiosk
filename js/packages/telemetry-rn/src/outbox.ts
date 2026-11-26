export interface TelemetryEvent {
  seq?: number;
  event_id: string;
  origin_device_id: string;
  origin_role: "tablet" | "server" | "game";
  origin_boot_id: string;
  origin_seq: number;
  category: "wifi" | "battery" | "ws" | "launcher" | "form" | "board" | "audit" | "system";
  kind: string;
  severity: "debug" | "info" | "warn" | "error" | "critical";
  occurred_at_ms: number;
  payload: Record<string, unknown>;
  relayed_from?: string;
  cloud_synced_at_ms?: number | null;
}

/**
 * Minimal SQLite driver contract matching expo-sqlite's async API (allowing mock injection in tests).
 */
export interface SQLiteDriver {
  execAsync(sql: string): Promise<void>;
  runAsync(sql: string, ...params: unknown[]): Promise<{ lastInsertRowId?: number; changes?: number }>;
  getAllAsync<T>(sql: string, ...params: unknown[]): Promise<T[]>;
  getFirstAsync<T>(sql: string, ...params: unknown[]): Promise<T | null>;
}

const CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/**
 * Generates a 26-char time-ordered ULID compatible with Go's oklog/ulid.
 */
export function newEventId(nowMs = Date.now()): string {
  let ts = nowMs;
  const timeChars = new Array<string>(10);
  for (let i = 9; i >= 0; i--) {
    timeChars[i] = CROCKFORD[ts % 32];
    ts = Math.floor(ts / 32);
  }
  const randBytes = new Uint8Array(10);
  if (typeof crypto !== "undefined" && crypto.getRandomValues) {
    crypto.getRandomValues(randBytes);
  } else {
    for (let i = 0; i < randBytes.length; i++) randBytes[i] = Math.floor(Math.random() * 256);
  }
  let randStr = "";
  for (let i = 0; i < 16; i++) {
    randStr += CROCKFORD[randBytes[i % 10] & 31];
  }
  return timeChars.join("") + randStr;
}

export class LocalTelemetryOutbox {
  private seqCounter = 0;
  private initialized = false;

  constructor(
    private readonly db: SQLiteDriver,
    private readonly originDeviceId: string,
    private readonly bootId: string
  ) {}

  async init(): Promise<void> {
    if (this.initialized) return;
    await this.db.execAsync(`
      CREATE TABLE IF NOT EXISTS telemetry_outbox (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        event_id TEXT NOT NULL UNIQUE,
        origin_device_id TEXT NOT NULL,
        origin_role TEXT NOT NULL,
        origin_boot_id TEXT NOT NULL,
        origin_seq INTEGER NOT NULL,
        category TEXT NOT NULL,
        kind TEXT NOT NULL,
        severity TEXT NOT NULL DEFAULT 'info',
        occurred_at_ms INTEGER NOT NULL,
        payload_json TEXT NOT NULL,
        relayed_from TEXT,
        relayed_to_server_at_ms INTEGER,
        cloud_synced_at_ms INTEGER
      );
      CREATE INDEX IF NOT EXISTS idx_rn_outbox_unsynced ON telemetry_outbox (cloud_synced_at_ms, seq);
      CREATE TABLE IF NOT EXISTS telemetry_cursors (
        key TEXT PRIMARY KEY,
        seq INTEGER NOT NULL DEFAULT 0
      );
    `);
    const row = await this.db.getFirstAsync<{ m: number | null }>(
      "SELECT MAX(origin_seq) AS m FROM telemetry_outbox WHERE origin_device_id = ? AND origin_boot_id = ?",
      this.originDeviceId,
      this.bootId
    );
    this.seqCounter = Number(row?.m ?? 0);
    this.initialized = true;
  }

  async record(input: {
    category: TelemetryEvent["category"];
    kind: string;
    severity?: TelemetryEvent["severity"];
    payload?: Record<string, unknown>;
  }): Promise<TelemetryEvent> {
    await this.init();
    const ev: TelemetryEvent = {
      event_id: newEventId(),
      origin_device_id: this.originDeviceId,
      origin_role: "tablet",
      origin_boot_id: this.bootId,
      origin_seq: ++this.seqCounter,
      category: input.category,
      kind: input.kind,
      severity: input.severity ?? "info",
      occurred_at_ms: Date.now(),
      payload: input.payload ?? {},
    };
    await this.db.runAsync(
      `INSERT OR IGNORE INTO telemetry_outbox
       (event_id, origin_device_id, origin_role, origin_boot_id, origin_seq, category, kind, severity, occurred_at_ms, payload_json)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      ev.event_id,
      ev.origin_device_id,
      ev.origin_role,
      ev.origin_boot_id,
      ev.origin_seq,
      ev.category,
      ev.kind,
      ev.severity,
      ev.occurred_at_ms,
      JSON.stringify(ev.payload)
    );
    return ev;
  }

  async cacheServerEvents(serverId: string, items: TelemetryEvent[]): Promise<void> {
    await this.init();
    for (const ev of items) {
      if (!ev.event_id || !ev.kind) continue;
      await this.db.runAsync(
        `INSERT OR IGNORE INTO telemetry_outbox
         (event_id, origin_device_id, origin_role, origin_boot_id, origin_seq, category, kind, severity, occurred_at_ms, payload_json, relayed_from)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ev.event_id,
        ev.origin_device_id,
        ev.origin_role,
        ev.origin_boot_id,
        ev.origin_seq,
        ev.category,
        ev.kind,
        ev.severity,
        ev.occurred_at_ms,
        JSON.stringify(ev.payload ?? {}),
        serverId
      );
    }
  }

  async getPendingForServerRelay(limit = 100): Promise<TelemetryEvent[]> {
    await this.init();
    const rows = await this.db.getAllAsync<Record<string, unknown>>(
      `SELECT * FROM telemetry_outbox
       WHERE relayed_from IS NULL AND relayed_to_server_at_ms IS NULL AND cloud_synced_at_ms IS NULL
       ORDER BY seq ASC LIMIT ?`,
      limit
    );
    return rows.map(rowToEvent);
  }

  async getPendingForCloud(limit = 150): Promise<TelemetryEvent[]> {
    await this.init();
    const rows = await this.db.getAllAsync<Record<string, unknown>>(
      `SELECT * FROM telemetry_outbox
       WHERE cloud_synced_at_ms IS NULL
       ORDER BY seq ASC LIMIT ?`,
      limit
    );
    return rows.map(rowToEvent);
  }

  async markRelayedToServer(eventIds: string[]): Promise<void> {
    if (!eventIds.length) return;
    await this.init();
    const now = Date.now();
    const placeholders = eventIds.map(() => "?").join(",");
    await this.db.runAsync(
      `UPDATE telemetry_outbox SET relayed_to_server_at_ms = ? WHERE event_id IN (${placeholders})`,
      now,
      ...eventIds
    );
  }

  async markCloudSynced(eventIds: string[]): Promise<void> {
    if (!eventIds.length) return;
    await this.init();
    const now = Date.now();
    const placeholders = eventIds.map(() => "?").join(",");
    await this.db.runAsync(
      `UPDATE telemetry_outbox SET cloud_synced_at_ms = ? WHERE event_id IN (${placeholders})`,
      now,
      ...eventIds
    );
  }

  async getCursor(key: string): Promise<number> {
    await this.init();
    const row = await this.db.getFirstAsync<{ seq: number }>("SELECT seq FROM telemetry_cursors WHERE key = ?", key);
    return Number(row?.seq ?? 0);
  }

  async setCursor(key: string, seq: number): Promise<void> {
    await this.init();
    await this.db.runAsync(
      "INSERT INTO telemetry_cursors (key, seq) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET seq = excluded.seq",
      key,
      seq
    );
  }
}

function rowToEvent(r: Record<string, unknown>): TelemetryEvent {
  let payload: Record<string, unknown> = {};
  try {
    payload = JSON.parse(String(r.payload_json || "{}")) as Record<string, unknown>;
  } catch {
    payload = {};
  }
  return {
    seq: Number(r.seq ?? 0),
    event_id: String(r.event_id),
    origin_device_id: String(r.origin_device_id),
    origin_role: (r.origin_role as TelemetryEvent["origin_role"]) || "tablet",
    origin_boot_id: String(r.origin_boot_id || ""),
    origin_seq: Number(r.origin_seq ?? 0),
    category: (r.category as TelemetryEvent["category"]) || "system",
    kind: String(r.kind),
    severity: (r.severity as TelemetryEvent["severity"]) || "info",
    occurred_at_ms: Number(r.occurred_at_ms ?? 0),
    payload,
    relayed_from: r.relayed_from ? String(r.relayed_from) : undefined,
    cloud_synced_at_ms: r.cloud_synced_at_ms ? Number(r.cloud_synced_at_ms) : null,
  };
}
