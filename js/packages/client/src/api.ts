import { CK_FRAMEWORK_VERSION, CK_PROTOCOL_VERSION } from "./generated/version.js";
import type {
  ActionResult,
  BoardView,
  ConfigResponse,
  DeviceInfo,
  DeviceRecord,
  HealthResponse,
  OperatorLoginResponse,
  SchemaSnapshot,
  Snapshot,
  SubmissionRecord,
} from "./generated/types.js";
import { signUtf8, verifyUtf8, type ClientKeyPair } from "./crypto.js";

export class KioskApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly details?: unknown
  ) {
    super(message);
    this.name = "KioskApiError";
  }
}

export class KioskApiClient {
  private deviceToken = "";
  private operatorToken = "";

  constructor(private baseUrl: string) {
    this.baseUrl = baseUrl.replace(/\/$/, "");
  }

  setBaseUrl(url: string): void {
    this.baseUrl = url.replace(/\/$/, "");
  }

  setDeviceToken(token: string): void {
    this.deviceToken = token;
  }

  setOperatorToken(token: string): void {
    this.operatorToken = token;
  }

  clearOperatorToken(): void {
    this.operatorToken = "";
  }

  private async request<T>(path: string, init: RequestInit = {}, auth: "none" | "device" | "operator" = "device"): Promise<T> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...((init.headers as Record<string, string>) || {}),
    };
    if (auth === "device" || auth === "operator") {
      headers["X-Device-Token"] = this.deviceToken;
    }
    if (auth === "operator") {
      headers["Authorization"] = `Bearer ${this.operatorToken}`;
    }
    const res = await fetch(`${this.baseUrl}${path}`, { ...init, headers });
    const data = (await res.json().catch(() => ({}))) as Record<string, unknown>;
    if (!res.ok) {
      throw new KioskApiError(
        res.status,
        String(data.code || "http_error"),
        String(data.error || `HTTP ${res.status}`),
        data.details
      );
    }
    return data as T;
  }

  health(): Promise<HealthResponse> {
    return this.request<HealthResponse>("/v1/health", { method: "GET" }, "none");
  }

  async pair(
    keyPair: ClientKeyPair,
    pairCode: string,
    deviceInfo: DeviceInfo
  ): Promise<{ device_id: string; server_id: string; server_pub: string; device_token: string }> {
    const h = await this.health();
    const nonce = crypto.randomUUID();
    const proof = await signUtf8(keyPair.privateKey, h.server_pub + nonce);
    const res = await this.request<{
      device_id: string;
      server_id: string;
      server_pub: string;
      server_sig: string;
      device_token: string;
    }>(
      "/v1/pair",
      {
        method: "POST",
        body: JSON.stringify({
          client_pub: keyPair.publicKeyBase64,
          pair_code: pairCode,
          nonce,
          proof,
          device_info: {
            ...deviceInfo,
            framework_version: deviceInfo.framework_version ?? CK_FRAMEWORK_VERSION,
            protocol_version: deviceInfo.protocol_version ?? CK_PROTOCOL_VERSION,
          },
        }),
      },
      "none"
    );
    const valid = await verifyUtf8(res.server_pub, keyPair.publicKeyBase64 + res.server_id, res.server_sig);
    if (!valid) {
      throw new KioskApiError(401, "invalid_server_sig", "Server identity signature failed verification");
    }
    this.deviceToken = res.device_token;
    return res;
  }

  async authenticateWithKey(
    keyPair: ClientKeyPair,
    pinnedServerPub: string,
    deviceInfo?: DeviceInfo
  ): Promise<{ device_token: string; snapshot: Snapshot }> {
    const chal = await this.request<{ nonce: string; server_id: string; server_pub: string; server_sig: string }>(
      "/v1/auth/challenge",
      { method: "POST" },
      "none"
    );
    if (chal.server_pub !== pinnedServerPub) {
      throw new KioskApiError(403, "server_key_mismatch", "Connected server key does not match pinned installation");
    }
    const serverOK = await verifyUtf8(chal.server_pub, chal.nonce, chal.server_sig);
    if (!serverOK) {
      throw new KioskApiError(401, "invalid_server_sig", "Server challenge signature invalid");
    }
    const sig = await signUtf8(keyPair.privateKey, chal.nonce + chal.server_pub);
    const res = await this.request<{ device_token: string; snapshot: Snapshot }>(
      "/v1/auth/verify",
      {
        method: "POST",
        body: JSON.stringify({
          client_id: keyPair.deviceId,
          nonce: chal.nonce,
          sig,
          device_info: deviceInfo
            ? {
                ...deviceInfo,
                framework_version: deviceInfo.framework_version ?? CK_FRAMEWORK_VERSION,
                protocol_version: deviceInfo.protocol_version ?? CK_PROTOCOL_VERSION,
              }
            : undefined,
        }),
      },
      "none"
    );
    this.deviceToken = res.device_token;
    return res;
  }

  schema(): Promise<SchemaSnapshot> {
    return this.request<SchemaSnapshot>("/v1/schema");
  }

  state(): Promise<Snapshot> {
    return this.request<Snapshot>("/v1/state");
  }

  submitForm(requestId: string, fields: Record<string, unknown>, formId = "", armRound = true): Promise<Record<string, unknown>> {
    return this.request(armRound ? "/v1/rounds" : "/v1/submissions", {
      method: "POST",
      body: JSON.stringify({ request_id: requestId, form_id: formId, fields }),
    });
  }

  cancelRound(roundId: string): Promise<{ ok: boolean }> {
    return this.request(`/v1/rounds/${encodeURIComponent(roundId)}`, { method: "DELETE" });
  }

  getBoard(boardId: string): Promise<BoardView> {
    return this.request(`/v1/boards/${encodeURIComponent(boardId)}`);
  }

  async operatorLogin(username: string, password: string): Promise<OperatorLoginResponse> {
    const res = await this.request<OperatorLoginResponse>(
      "/v1/operators/login",
      { method: "POST", body: JSON.stringify({ username, password }) },
      "device"
    );
    this.operatorToken = res.access_token;
    return res;
  }

  getConfig(): Promise<ConfigResponse> {
    return this.request<ConfigResponse>("/v1/config", { method: "GET" }, "operator");
  }

  patchConfig(values: Record<string, unknown>, expectedRevision?: number): Promise<ConfigResponse> {
    return this.request<ConfigResponse>(
      "/v1/config",
      { method: "PATCH", body: JSON.stringify({ expected_revision: expectedRevision, values }) },
      "operator"
    );
  }

  triggerAction(actionId: string, params: Record<string, unknown> = {}): Promise<ActionResult> {
    return this.request<ActionResult>(
      `/v1/actions/${encodeURIComponent(actionId)}`,
      { method: "POST", body: JSON.stringify({ params }) },
      "operator"
    );
  }

  deleteBoardRow(boardId: string, entryId: string): Promise<{ ok: boolean }> {
    return this.request(
      `/v1/boards/${encodeURIComponent(boardId)}/rows/${encodeURIComponent(entryId)}`,
      { method: "DELETE" },
      "operator"
    );
  }

  listSubmissions(params: { formId?: string; q?: string; limit?: number; offset?: number } = {}): Promise<{
    items: SubmissionRecord[];
    total: number;
  }> {
    const qs = new URLSearchParams();
    if (params.formId) qs.set("form_id", params.formId);
    if (params.q) qs.set("q", params.q);
    if (params.limit) qs.set("limit", String(params.limit));
    if (params.offset) qs.set("offset", String(params.offset));
    return this.request(`/v1/submissions?${qs.toString()}`, { method: "GET" }, "operator");
  }

  listDevices(): Promise<{ devices: DeviceRecord[] }> {
    return this.request("/v1/devices", { method: "GET" }, "operator");
  }
}
