import { CK_FRAMEWORK_VERSION, CK_PROTOCOL_VERSION } from "./generated/version.js";
import type { BoardView, DeviceInfo, SettingSpec, Snapshot } from "./generated/types.js";
import { signUtf8, verifyUtf8, type ClientKeyPair } from "./crypto.js";

export interface KioskSocketEvents {
  onStatus?: (status: "connecting" | "connected" | "disconnected") => void;
  onAuthOk?: (payload: {
    device_token: string;
    snapshot: Snapshot;
    config_rev: number;
    values: Record<string, unknown>;
    runtime_values: Record<string, unknown>;
    settings: SettingSpec[];
  }) => void;
  onSnapshot?: (snapshot: Snapshot) => void;
  onConfig?: (payload: {
    revision: number;
    values: Record<string, unknown>;
    runtime_values: Record<string, unknown>;
    changed: string[];
    settings: SettingSpec[];
  }) => void;
  onBoardUpdated?: (boardId: string, board: BoardView) => void;
  onClockOffset?: (offsetMs: number, rttMs: number) => void;
  onError?: (err: Error) => void;
}

/**
 * Headless WebSocket client performing mutual Ed25519 challenge-response auth,
 * clock-offset sync pings, live operator elevation, and exponential backoff reconnects.
 */
export class KioskSocket {
  private ws: WebSocket | null = null;
  private closed = false;
  private attempt = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;

  constructor(
    private readonly opts: {
      baseUrl: string;
      keyPair: ClientKeyPair;
      pinnedServerPub: string;
      deviceInfo?: DeviceInfo;
    },
    private readonly events: KioskSocketEvents
  ) {}

  connect(): void {
    this.closed = false;
    this.events.onStatus?.("connecting");
    const wsUrl = this.opts.baseUrl.replace(/^https:/, "wss:").replace(/^http:/, "ws:").replace(/\/$/, "") + "/v1/events";
    const ws = new WebSocket(wsUrl);
    this.ws = ws;

    ws.onmessage = async (ev) => {
      const msg = JSON.parse(String(ev.data)) as Record<string, unknown>;
      switch (msg.t || msg.type) {
        case "challenge": {
          const serverPub = String(msg.server_pub ?? "");
          const nonce = String(msg.nonce ?? "");
          const serverSig = String(msg.server_sig ?? "");
          if (serverPub !== this.opts.pinnedServerPub) {
            this.events.onError?.(new Error("Server public key mismatch"));
            ws.close();
            return;
          }
          const ok = await verifyUtf8(serverPub, nonce, serverSig);
          if (!ok) {
            this.events.onError?.(new Error("Server challenge signature invalid"));
            ws.close();
            return;
          }
          const sig = await signUtf8(this.opts.keyPair.privateKey, nonce + serverPub);
          ws.send(
            JSON.stringify({
              t: "auth",
              client_id: this.opts.keyPair.deviceId,
              sig,
              device_info: this.opts.deviceInfo
                ? {
                    ...this.opts.deviceInfo,
                    framework_version: this.opts.deviceInfo.framework_version ?? CK_FRAMEWORK_VERSION,
                    protocol_version: this.opts.deviceInfo.protocol_version ?? CK_PROTOCOL_VERSION,
                  }
                : undefined,
            })
          );
          break;
        }
        case "auth_ok": {
          this.attempt = 0;
          this.events.onStatus?.("connected");
          this.events.onAuthOk?.(msg as never);
          this.startPing();
          break;
        }
        case "snapshot":
          this.events.onSnapshot?.(msg as unknown as Snapshot);
          break;
        case "config":
          this.events.onConfig?.(msg as never);
          break;
        case "board_updated":
          this.events.onBoardUpdated?.(String(msg.board_id), msg.board as BoardView);
          break;
        case "pong": {
          const clientSent = Number(msg.client_ms ?? 0);
          const serverMs = Number(msg.server_ms ?? 0);
          const now = Date.now();
          const rtt = Math.max(0, now - clientSent);
          const offset = serverMs - (clientSent + rtt / 2);
          this.events.onClockOffset?.(offset, rtt);
          break;
        }
      }
    };

    ws.onclose = () => {
      this.stopPing();
      this.events.onStatus?.("disconnected");
      if (!this.closed) {
        const delay = Math.min(1000 * Math.pow(2, this.attempt++), 15000);
        this.reconnectTimer = setTimeout(() => this.connect(), delay);
      }
    };
  }

  disconnect(): void {
    this.closed = true;
    this.stopPing();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
  }

  elevateOperator(jwtToken: string): void {
    this.send({ t: "operator_auth", token: jwtToken });
  }

  dropOperator(): void {
    this.send({ t: "operator_logout" });
  }

  sendConfigPatch(values: Record<string, unknown>): void {
    this.send({ t: "config_patch", values });
  }

  private send(payload: Record<string, unknown>): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(payload));
    }
  }

  private startPing(): void {
    this.stopPing();
    this.pingTimer = setInterval(() => {
      this.send({ t: "ping", cid: String(Date.now()), client_ms: Date.now() });
    }, 10000);
  }

  private stopPing(): void {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }
}
