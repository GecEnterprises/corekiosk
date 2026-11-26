import { CK_PROTOCOL_VERSION } from "../../js/packages/client/src/generated/version";

export interface BridgeHandlers {
  onHello?: (msg: Record<string, unknown>) => void;
  onConfig?: (values: Record<string, unknown>, runtimeValues: Record<string, unknown>, changed: string[], revision: number) => void;
  onArm?: (round: Record<string, unknown>) => void;
  onCancel?: (roundId: string, reason: string) => void;
  onSubmission?: (submissionId: string, fields: Record<string, unknown>) => void;
  onAction?: (actionId: string, params: Record<string, unknown>, callId: string) => void;
  onBoardRowRemoved?: (boardId: string, entryId: string) => void;
  onBoardSnapshot?: (boardId: string, rows: unknown[]) => void;
}

/**
 * Thin loopback WebSocket bridge adapter for HTML5 / Electron / WebGL kiosk games.
 */
export class CKWebBridge {
  private ws: WebSocket | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;

  constructor(
    private readonly opts: {
      serverUrl: string;
      bridgeToken?: string;
      gameName: string;
      gameVersion: string;
      engineVersion?: string;
      capabilities?: Record<string, unknown>;
    },
    private readonly handlers: BridgeHandlers
  ) {}

  connect(): void {
    this.closed = false;
    const base = this.opts.serverUrl.replace(/^https:/, "wss:").replace(/^http:/, "ws:").replace(/\/$/, "");
    const url = this.opts.bridgeToken
      ? `${base}/v1/bridge?token=${encodeURIComponent(this.opts.bridgeToken)}`
      : `${base}/v1/bridge`;

    this.ws = new WebSocket(url);
    this.ws.onopen = () => {
      this.send({
        t: "ready",
        game_name: this.opts.gameName,
        game_version: this.opts.gameVersion,
        engine_version: this.opts.engineVersion ?? "web",
        bridge_protocol: CK_PROTOCOL_VERSION,
        capabilities: this.opts.capabilities,
      });
    };
    this.ws.onmessage = (ev) => {
      const msg = JSON.parse(String(ev.data)) as Record<string, unknown>;
      switch (msg.t) {
        case "hello":
          this.handlers.onHello?.(msg);
          this.handlers.onConfig?.(
            (msg.values as Record<string, unknown>) ?? {},
            (msg.runtime_values as Record<string, unknown>) ?? {},
            [],
            Number(msg.revision ?? 0)
          );
          break;
        case "config":
          this.handlers.onConfig?.(
            (msg.values as Record<string, unknown>) ?? {},
            (msg.runtime_values as Record<string, unknown>) ?? {},
            (msg.changed as string[]) ?? [],
            Number(msg.revision ?? 0)
          );
          break;
        case "arm":
          this.handlers.onArm?.((msg.round as Record<string, unknown>) ?? {});
          break;
        case "cancel":
          this.handlers.onCancel?.(String(msg.round_id ?? ""), String(msg.reason ?? ""));
          break;
        case "submission":
          this.handlers.onSubmission?.(String(msg.submission_id ?? ""), (msg.params as Record<string, unknown>) ?? {});
          break;
        case "action":
          this.handlers.onAction?.(
            String(msg.action_id ?? ""),
            (msg.params as Record<string, unknown>) ?? {},
            String(msg.call_id ?? "")
          );
          break;
        case "board_row_removed":
          this.handlers.onBoardRowRemoved?.(String(msg.board_id ?? ""), String(msg.entry_id ?? ""));
          break;
        case "board_snapshot":
          this.handlers.onBoardSnapshot?.(String(msg.board_id ?? ""), (msg.rows as unknown[]) ?? []);
          break;
      }
    };
    this.ws.onclose = () => {
      if (!this.closed) {
        this.reconnectTimer = setTimeout(() => this.connect(), 1500);
      }
    };
  }

  disconnect(): void {
    this.closed = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
  }

  send(payload: Record<string, unknown>): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(payload));
    }
  }

  sendActionResult(callId: string, actionId: string, ok: boolean, message = "", data?: Record<string, unknown>): void {
    this.send({ t: "action_result", call_id: callId, action_id: actionId, ok, message, data });
  }
}
