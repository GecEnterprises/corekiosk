import { signUtf8, type ClientKeyPair } from "@corekiosk/client";
import type { LocalTelemetryOutbox, TelemetryEvent } from "./outbox.js";

export interface MuleSyncConfig {
  outbox: LocalTelemetryOutbox;
  keyPair: ClientKeyPair;
  getServerBaseUrl(): string;
  getDeviceToken(): string;
  getServerId(): string;
  getAppName(): string;
  isServerCloudOnline(): boolean;
  isTabletInternetReachable(): boolean;
  telemetryIngestUrl?: string;
  telemetryApiKey?: string;
  intervalMs?: number;
}

/**
 * Bidirectional local-first sync loop:
 * 1. Pushes tablet telemetry to ckserver on LAN (/v1/telemetry/ingest) so PC ships it.
 * 2. When PC is offline from cloud & tablet has internet (4G/5G or venue Wi-Fi),
 *    pulls PC outbox (/v1/telemetry/pull), caches locally, uploads Ed25519-signed batch
 *    to the configured HTTPS telemetry ingest endpoint, and ACKs back (/v1/telemetry/ack).
 */
export class TelemetryMuleSync {
  private timer: ReturnType<typeof setInterval> | null = null;
  private running = false;

  constructor(private readonly cfg: MuleSyncConfig) {}

  private get ingestUrl(): string | undefined {
    return this.cfg.telemetryIngestUrl;
  }

  private get apiKey(): string | undefined {
    return this.cfg.telemetryApiKey;
  }

  start(): void {
    this.stop();
    this.timer = setInterval(() => void this.syncOnce(), this.cfg.intervalMs ?? 20_000);
    void this.syncOnce();
  }

  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  async syncOnce(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      await this.pushToServerLAN();
      if (!this.cfg.isServerCloudOnline()) {
        await this.pullFromServerLAN();
      }
      if (this.ingestUrl && this.cfg.isTabletInternetReachable()) {
        const synced = await this.flushToCloud();
        if (synced.length > 0) {
          await this.ackServerLAN(synced);
        }
      }
    } finally {
      this.running = false;
    }
  }

  private async pushToServerLAN(): Promise<void> {
    const base = this.cfg.getServerBaseUrl();
    const token = this.cfg.getDeviceToken();
    if (!base || !token) return;
    const pending = await this.cfg.outbox.getPendingForServerRelay(100);
    if (!pending.length) return;
    try {
      const res = await fetch(`${base.replace(/\/$/, "")}/v1/telemetry/ingest`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Device-Token": token },
        body: JSON.stringify({ events: pending }),
      });
      if (!res.ok) return;
      const data = (await res.json()) as { accepted_ids?: string[] };
      if (data.accepted_ids?.length) {
        await this.cfg.outbox.markRelayedToServer(data.accepted_ids);
      }
    } catch {
      // LAN unreachable; keep rows in local SQLite outbox.
    }
  }

  private async pullFromServerLAN(): Promise<void> {
    const base = this.cfg.getServerBaseUrl();
    const token = this.cfg.getDeviceToken();
    const serverId = this.cfg.getServerId();
    if (!base || !token || !serverId) return;
    const cursorKey = `pull:${serverId}`;
    const afterSeq = await this.cfg.outbox.getCursor(cursorKey);
    try {
      const res = await fetch(`${base.replace(/\/$/, "")}/v1/telemetry/pull?after_seq=${afterSeq}&limit=200`, {
        headers: { "X-Device-Token": token },
      });
      if (!res.ok) return;
      const data = (await res.json()) as { items?: TelemetryEvent[]; max_seq?: number };
      if (data.items?.length) {
        await this.cfg.outbox.cacheServerEvents(serverId, data.items);
        if (data.max_seq && data.max_seq > afterSeq) {
          await this.cfg.outbox.setCursor(cursorKey, data.max_seq);
        }
      }
    } catch {
      // Retry next tick.
    }
  }

  private async flushToCloud(): Promise<string[]> {
    const url = this.ingestUrl;
    const serverId = this.cfg.getServerId();
    if (!url || !serverId) return [];
    const pending = await this.cfg.outbox.getPendingForCloud(150);
    if (!pending.length) return [];

    const envelope = {
      installation_id: serverId,
      app_name: this.cfg.getAppName(),
      sender_device_id: this.cfg.keyPair.deviceId,
      sent_at_ms: Date.now(),
      events: pending,
    };
    const raw = JSON.stringify(envelope);
    const sig = await signUtf8(this.cfg.keyPair.privateKey, raw);

    try {
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
        "X-CK-Device-ID": this.cfg.keyPair.deviceId,
        "X-CK-Installation-ID": serverId,
        "X-CK-Public-Key": this.cfg.keyPair.publicKeyBase64,
        "X-CK-Signature": sig,
      };
      if (this.apiKey) headers["X-CK-API-Key"] = this.apiKey;

      const res = await fetch(url, { method: "POST", headers, body: raw });
      if (!res.ok) return [];
      const data = (await res.json()) as { accepted_ids?: string[] };
      const accepted = data.accepted_ids ?? pending.map((e) => e.event_id);
      await this.cfg.outbox.markCloudSynced(accepted);
      return accepted;
    } catch {
      return [];
    }
  }

  private async ackServerLAN(syncedIds: string[]): Promise<void> {
    const base = this.cfg.getServerBaseUrl();
    const token = this.cfg.getDeviceToken();
    const serverId = this.cfg.getServerId();
    if (!base || !token || !serverId) return;
    const upToSeq = await this.cfg.outbox.getCursor(`pull:${serverId}`);
    try {
      await fetch(`${base.replace(/\/$/, "")}/v1/telemetry/ack`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Device-Token": token },
        body: JSON.stringify({ synced_event_ids: syncedIds, up_to_seq: upToSeq }),
      });
    } catch {
      // Safe to skip; the cloud telemetry endpoint deduplicates idempotently on retry.
    }
  }
}
