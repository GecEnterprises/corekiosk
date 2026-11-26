/**
 * corekiosk Serverless Telemetry & Form-Sync Worker (Cloudflare Workers + D1 SQLite + optional R2 / Analytics Engine)
 *
 * Endpoints:
 *   GET  /health                                      - Liveness check
 *   POST /v1/ingest (or POST /)                       - Gzipped + Ed25519-signed batch ingest from Kiosk PC or Tablet Mule
 *   PUT  /v1/backups/:installation_id/:filename       - Optional R2 upload for SQLite VACUUM .db.gz backups
 *   GET  /admin                                       - Built-in Braun/Paper operator & client reporting console
 *   GET  /v1/admin/installations                      - Fleet overview across all installations
 *   GET  /v1/admin/installations/:id/summary          - Prize redemption tally, active devices & recent alerts
 *   GET  /v1/admin/installations/:id/submissions      - Paginated JSON player submissions + outcomes
 *   GET  /v1/admin/installations/:id/submissions.csv  - RFC-4180 CSV export of leads, scores, and won prizes
 */

export interface D1ResultLike<T = Record<string, unknown>> {
  results?: T[];
  success: boolean;
}

export interface D1PreparedStatementLike {
  bind(...values: unknown[]): D1PreparedStatementLike;
  first<T = Record<string, unknown>>(colName?: string): Promise<T | null>;
  all<T = Record<string, unknown>>(): Promise<D1ResultLike<T>>;
  run(): Promise<D1ResultLike>;
}

export interface D1DatabaseLike {
  prepare(query: string): D1PreparedStatementLike;
  batch<T = unknown>(statements: D1PreparedStatementLike[]): Promise<D1ResultLike<T>[]>;
}

export interface R2BucketLike {
  put(key: string, value: ReadableStream | ArrayBuffer | Uint8Array | string, options?: Record<string, unknown>): Promise<unknown>;
}

export interface AnalyticsEngineLike {
  writeDataPoint(event: { blobs?: string[]; doubles?: number[]; indexes?: string[] }): void;
}

export interface Env {
  DB: D1DatabaseLike;
  BACKUPS?: R2BucketLike;
  METRICS?: AnalyticsEngineLike;
  CK_TELEMETRY_API_KEY?: string;
  CK_ADMIN_TOKEN?: string;
  CK_TRUST_CF_ACCESS?: string;
}

export interface TelemetryEvent {
  event_id: string;
  origin_device_id: string;
  origin_role: string;
  origin_boot_id?: string;
  origin_seq?: number;
  category: string;
  kind: string;
  severity?: string;
  occurred_at_ms: number;
  payload?: Record<string, unknown>;
  relayed_from?: string;
}

export interface BatchEnvelope {
  installation_id: string;
  app_name?: string;
  sender_device_id: string;
  sent_at_ms?: number;
  events: TelemetryEvent[];
}

const CORS_HEADERS: Record<string, string> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, POST, PUT, OPTIONS",
  "Access-Control-Allow-Headers":
    "Content-Type, Content-Encoding, Authorization, X-CK-API-Key, X-CK-Device-ID, X-CK-Installation-ID, X-CK-Public-Key, X-CK-Signature, X-CK-Timestamp",
};

const MAX_BATCH_STATEMENT_CHUNK = 80;

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    if (req.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: CORS_HEADERS });
    }

    const url = new URL(req.url);
    const path = url.pathname.replace(/\/+$/, "") || "/";

    try {
      if (req.method === "GET" && path === "/health") {
        return json({ ok: true, service: "corekiosk-telemetry-worker", server_ms: Date.now() });
      }

      if (req.method === "POST" && (path === "/v1/ingest" || path === "/")) {
        return await handleIngest(req, env);
      }

      if (req.method === "PUT" && path.startsWith("/v1/backups/")) {
        return await handleBackupUpload(req, env, path);
      }

      if (req.method === "GET" && path === "/admin") {
        return new Response(ADMIN_DASHBOARD_HTML, {
          status: 200,
          headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" },
        });
      }

      if (path.startsWith("/v1/admin")) {
        const authErr = checkAdminAuth(req, url, env);
        if (authErr) return authErr;

        if (req.method === "GET" && path === "/v1/admin/installations") {
          return await handleListInstallations(env);
        }

        const summaryMatch = path.match(/^\/v1\/admin\/installations\/([^/]+)\/summary$/);
        if (req.method === "GET" && summaryMatch) {
          return await handleInstallationSummary(env, decodeURIComponent(summaryMatch[1]));
        }

        const subsMatch = path.match(/^\/v1\/admin\/installations\/([^/]+)\/submissions$/);
        if (req.method === "GET" && subsMatch) {
          return await handleListSubmissions(env, decodeURIComponent(subsMatch[1]), url);
        }

        const csvMatch = path.match(/^\/v1\/admin\/installations\/([^/]+)\/submissions\.csv$/);
        if (req.method === "GET" && csvMatch) {
          return await handleExportSubmissionsCSV(env, decodeURIComponent(csvMatch[1]), url);
        }
      }

      return json({ error: "not found" }, 404);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      return json({ error: msg }, 500);
    }
  },
};

async function handleIngest(req: Request, env: Env): Promise<Response> {
  if (env.CK_TELEMETRY_API_KEY) {
    const provided = req.headers.get("x-ck-api-key") ?? "";
    if (!timingSafeEqualStr(provided, env.CK_TELEMETRY_API_KEY)) {
      return json({ error: "invalid api key" }, 401);
    }
  }

  let rawBytes: Uint8Array;
  try {
    rawBytes = await readMaybeGzippedBody(req, 10 * 1024 * 1024);
  } catch (err) {
    return json({ error: `invalid request body: ${err instanceof Error ? err.message : String(err)}` }, 400);
  }

  let envelope: BatchEnvelope;
  try {
    envelope = JSON.parse(new TextDecoder().decode(rawBytes)) as BatchEnvelope;
  } catch {
    return json({ error: "invalid json payload" }, 400);
  }

  const installationId = String(envelope.installation_id ?? "").trim();
  if (!installationId || !Array.isArray(envelope.events)) {
    return json({ error: "installation_id and events array are required" }, 400);
  }

  const senderDeviceId = (req.headers.get("x-ck-device-id") || envelope.sender_device_id || "").trim();
  const sigB64 = (req.headers.get("x-ck-signature") || "").trim();
  const headerPubB64 = (req.headers.get("x-ck-public-key") || "").trim();

  // Verify Ed25519 batch authenticity using pinned device key in D1 or self-certifying X-CK-Public-Key.
  let verifiedSenderPubB64 = "";
  const existingDevice = senderDeviceId
    ? await env.DB.prepare("SELECT public_key FROM devices WHERE installation_id = ?1 AND device_id = ?2")
        .bind(installationId, senderDeviceId)
        .first<{ public_key: string }>()
    : null;

  const pinnedPubB64 = existingDevice?.public_key || "";
  const candidatePubB64 = pinnedPubB64 || headerPubB64;

  if (candidatePubB64) {
    if (!sigB64) {
      return json({ error: "missing x-ck-signature for registered device" }, 401);
    }
    const pubBytes = decodeBase64(candidatePubB64);
    if (!pubBytes || pubBytes.length !== 32) {
      return json({ error: "invalid ed25519 public key" }, 401);
    }
    const derivedId = await deriveDeviceId(pubBytes);
    if (senderDeviceId && derivedId !== senderDeviceId) {
      return json({ error: "public key fingerprint does not match sender device_id" }, 401);
    }
    if (pinnedPubB64 && headerPubB64 && pinnedPubB64 !== headerPubB64) {
      return json({ error: "public key mismatch against pinned device identity" }, 401);
    }
    const sigBytes = decodeBase64(sigB64);
    if (!sigBytes || sigBytes.length !== 64) {
      return json({ error: "invalid signature encoding" }, 401);
    }
    const valid = await verifyEd25519(pubBytes, rawBytes, sigBytes);
    if (!valid) {
      return json({ error: "invalid ed25519 batch signature" }, 401);
    }
    verifiedSenderPubB64 = candidatePubB64;
  }

  const nowMs = Date.now();
  const appName = String(envelope.app_name ?? "");
  const statements: D1PreparedStatementLike[] = [];

  // 1. Upsert installation heartbeat
  statements.push(
    env.DB.prepare(
      `INSERT INTO installations (installation_id, app_name, last_sender_id, last_seen_ms, created_at_ms)
       VALUES (?1, ?2, ?3, ?4, ?4)
       ON CONFLICT(installation_id) DO UPDATE SET
         app_name = CASE WHEN excluded.app_name != '' THEN excluded.app_name ELSE installations.app_name END,
         last_sender_id = excluded.last_sender_id,
         last_seen_ms = MAX(installations.last_seen_ms, excluded.last_seen_ms)`
    ).bind(installationId, appName, senderDeviceId, nowMs)
  );

  // Pin verified sender public key immediately (Trust-On-First-Use + self-certifying sha256[:8])
  if (senderDeviceId && verifiedSenderPubB64) {
    const inferredRole = senderDeviceId === installationId ? "server" : "tablet";
    statements.push(
      env.DB.prepare(
        `INSERT INTO devices (installation_id, device_id, public_key, role, updated_at_ms)
         VALUES (?1, ?2, ?3, ?4, ?5)
         ON CONFLICT(installation_id, device_id) DO UPDATE SET
           public_key = CASE WHEN devices.public_key = '' THEN excluded.public_key ELSE devices.public_key END,
           updated_at_ms = MAX(devices.updated_at_ms, excluded.updated_at_ms)`
      ).bind(installationId, senderDeviceId, verifiedSenderPubB64, inferredRole, nowMs)
    );
  }

  const accepted: string[] = [];

  for (const ev of envelope.events) {
    if (!ev || typeof ev.event_id !== "string" || !ev.event_id || !ev.kind) {
      continue;
    }
    const payload = ev.payload ?? {};
    const occurredAt = Number(ev.occurred_at_ms || nowMs);
    const bootId = String(ev.origin_boot_id ?? "");
    const seq = Number(ev.origin_seq ?? 0);

    // Always append to immutable deduplicated events ledger
    statements.push(
      env.DB.prepare(
        `INSERT INTO events (
           event_id, installation_id, app_name, origin_device_id, origin_role,
           origin_boot_id, origin_seq, category, kind, severity,
           occurred_at_ms, received_at_ms, relayed_from, payload_json
         ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)
         ON CONFLICT(event_id) DO NOTHING`
      ).bind(
        ev.event_id,
        installationId,
        appName,
        String(ev.origin_device_id ?? senderDeviceId),
        String(ev.origin_role ?? "server"),
        bootId,
        seq,
        String(ev.category ?? "system"),
        ev.kind,
        String(ev.severity ?? "info"),
        occurredAt,
        nowMs,
        String(ev.relayed_from ?? ""),
        JSON.stringify(payload)
      )
    );

    // Domain projections (conflict-free & arrival-order independent)
    if (ev.kind === "form.submission") {
      const formId = String(payload.form_id || "registration");
      const submissionId = String(payload.submission_id || ev.event_id);
      const fieldsJson = JSON.stringify(payload.fields ?? {});

      if (payload.form_schema) {
        statements.push(
          env.DB.prepare(
            `INSERT INTO form_schemas (installation_id, form_id, schema_json, updated_at_ms)
             VALUES (?1, ?2, ?3, ?4)
             ON CONFLICT(installation_id, form_id) DO UPDATE SET
               schema_json = excluded.schema_json,
               updated_at_ms = excluded.updated_at_ms`
          ).bind(installationId, formId, JSON.stringify(payload.form_schema), nowMs)
        );
      }

      statements.push(
        env.DB.prepare(
          `INSERT INTO form_submissions (
             submission_id, installation_id, app_name, form_id, form_version,
             source_device_id, source_device_name, round_id, status,
             fields_json, created_at_ms, updated_at_ms
           ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
           ON CONFLICT(submission_id) DO UPDATE SET
             app_name = CASE WHEN excluded.app_name != '' THEN excluded.app_name ELSE form_submissions.app_name END,
             form_id = excluded.form_id,
             form_version = excluded.form_version,
             source_device_id = excluded.source_device_id,
             source_device_name = excluded.source_device_name,
             round_id = CASE WHEN excluded.round_id != '' THEN excluded.round_id ELSE form_submissions.round_id END,
             fields_json = excluded.fields_json,
             created_at_ms = MIN(form_submissions.created_at_ms, excluded.created_at_ms),
             updated_at_ms = MAX(form_submissions.updated_at_ms, excluded.updated_at_ms)`
        ).bind(
          submissionId,
          installationId,
          appName,
          formId,
          Number(payload.form_version ?? 1),
          String(payload.source_device_id ?? ev.origin_device_id ?? ""),
          String(payload.source_device_name ?? ""),
          String(payload.round_id ?? ""),
          String(payload.status ?? "submitted"),
          fieldsJson,
          occurredAt,
          nowMs
        )
      );
    } else if (ev.kind === "form.outcome") {
      const submissionId = String(payload.submission_id ?? "");
      if (submissionId) {
        const outcome = (payload.outcome as Record<string, unknown> | undefined) ?? {};
        const scoreVal = typeof outcome.score === "number" ? Math.round(outcome.score) : null;
        const rewardId = typeof outcome.reward_id === "string" ? outcome.reward_id : "";
        const rewardTitle = typeof outcome.reward_title === "string" ? outcome.reward_title : "";
        statements.push(
          env.DB.prepare(
            `INSERT INTO form_submissions (
               submission_id, installation_id, app_name, form_id, round_id,
               status, score, reward_id, reward_title, outcome_json, created_at_ms, updated_at_ms
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
             ON CONFLICT(submission_id) DO UPDATE SET
               round_id = CASE WHEN excluded.round_id != '' THEN excluded.round_id ELSE form_submissions.round_id END,
               status = excluded.status,
               score = COALESCE(excluded.score, form_submissions.score),
               reward_id = CASE WHEN excluded.reward_id != '' THEN excluded.reward_id ELSE form_submissions.reward_id END,
               reward_title = CASE WHEN excluded.reward_title != '' THEN excluded.reward_title ELSE form_submissions.reward_title END,
               outcome_json = excluded.outcome_json,
               updated_at_ms = MAX(form_submissions.updated_at_ms, excluded.updated_at_ms)`
          ).bind(
            submissionId,
            installationId,
            appName,
            String(payload.form_id || "registration"),
            String(payload.round_id ?? ""),
            String(payload.status ?? "played"),
            scoreVal,
            rewardId,
            rewardTitle,
            JSON.stringify(outcome),
            occurredAt,
            nowMs
          )
        );
      }
    } else if (ev.kind === "device.paired") {
      const devId = String(payload.device_id ?? "");
      if (devId) {
        statements.push(
          env.DB.prepare(
            `INSERT INTO devices (
               installation_id, device_id, public_key, role, device_name, device_type,
               model, os_name, os_version, app_version, updated_at_ms
             ) VALUES (?1, ?2, ?3, 'tablet', ?4, ?5, ?6, ?7, ?8, ?9, ?10)
             ON CONFLICT(installation_id, device_id) DO UPDATE SET
               public_key = CASE WHEN devices.public_key = '' THEN excluded.public_key ELSE devices.public_key END,
               device_name = CASE WHEN excluded.device_name != '' THEN excluded.device_name ELSE devices.device_name END,
               device_type = CASE WHEN excluded.device_type != '' THEN excluded.device_type ELSE devices.device_type END,
               model = CASE WHEN excluded.model != '' THEN excluded.model ELSE devices.model END,
               os_name = CASE WHEN excluded.os_name != '' THEN excluded.os_name ELSE devices.os_name END,
               os_version = CASE WHEN excluded.os_version != '' THEN excluded.os_version ELSE devices.os_version END,
               app_version = CASE WHEN excluded.app_version != '' THEN excluded.app_version ELSE devices.app_version END,
               updated_at_ms = MAX(devices.updated_at_ms, excluded.updated_at_ms)`
          ).bind(
            installationId,
            devId,
            String(payload.public_key ?? ""),
            String(payload.device_name ?? ""),
            String(payload.device_type ?? ""),
            String(payload.model ?? ""),
            String(payload.os_name ?? ""),
            String(payload.os_version ?? ""),
            String(payload.app_version ?? ""),
            nowMs
          )
        );
      }
    } else if (ev.kind === "device.profile" || ev.kind === "device.status" || ev.kind === "system.vitals") {
      const devId = String(payload.device_id ?? ev.origin_device_id ?? senderDeviceId);
      if (devId) {
        statements.push(
          env.DB.prepare(
            `INSERT INTO devices (
               installation_id, device_id, role, device_name, state_json,
               origin_boot_id, origin_seq, origin_occurred_at_ms, updated_at_ms
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
             ON CONFLICT(installation_id, device_id) DO UPDATE SET
               state_json = excluded.state_json,
               origin_boot_id = excluded.origin_boot_id,
               origin_seq = excluded.origin_seq,
               origin_occurred_at_ms = excluded.origin_occurred_at_ms,
               updated_at_ms = excluded.updated_at_ms
             WHERE (excluded.origin_boot_id = devices.origin_boot_id AND excluded.origin_seq > devices.origin_seq)
                OR (excluded.origin_boot_id != devices.origin_boot_id AND excluded.origin_occurred_at_ms >= devices.origin_occurred_at_ms)`
          ).bind(
            installationId,
            devId,
            String(ev.origin_role ?? "server"),
            String(payload.device_name ?? ""),
            JSON.stringify(payload),
            bootId,
            seq,
            occurredAt,
            nowMs
          )
        );
      }
    } else if (ev.kind === "board.entry_added") {
      const boardId = String(payload.board_id || "default");
      const entryId = String(payload.entry_id || ev.event_id);
      statements.push(
        env.DB.prepare(
          `INSERT INTO board_entries (
             installation_id, board_id, entry_id, submission_id, sort_value, display_json, created_at_ms
           ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
           ON CONFLICT(installation_id, board_id, entry_id) DO UPDATE SET
             submission_id = CASE WHEN board_entries.submission_id = '' THEN excluded.submission_id ELSE board_entries.submission_id END,
             sort_value = excluded.sort_value,
             display_json = excluded.display_json,
             created_at_ms = CASE WHEN board_entries.created_at_ms = 0 THEN excluded.created_at_ms ELSE board_entries.created_at_ms END`
        ).bind(
          installationId,
          boardId,
          entryId,
          String(payload.submission_id ?? ""),
          Number(payload.sort_value ?? 0),
          JSON.stringify(payload.display ?? {}),
          Number(payload.created_at_ms ?? occurredAt)
        )
      );
    } else if (ev.kind === "board.entry_removed") {
      const boardId = String(payload.board_id || "default");
      const entryId = String(payload.entry_id || "");
      if (entryId) {
        // 2P-Set CRDT: tombstones stick permanently regardless of arrival order
        statements.push(
          env.DB.prepare(
            `INSERT INTO board_entries (
               installation_id, board_id, entry_id, removed_at_ms, removed_by_operator_id
             ) VALUES (?1, ?2, ?3, ?4, ?5)
             ON CONFLICT(installation_id, board_id, entry_id) DO UPDATE SET
               removed_at_ms = COALESCE(board_entries.removed_at_ms, excluded.removed_at_ms),
               removed_by_operator_id = CASE WHEN board_entries.removed_by_operator_id = '' THEN excluded.removed_by_operator_id ELSE board_entries.removed_by_operator_id END`
          ).bind(
            installationId,
            boardId,
            entryId,
            Number(payload.removed_at_ms ?? occurredAt),
            String(payload.removed_by_operator_id ?? "")
          )
        );
      }
    }

    // Optional Workers Analytics Engine time-series stream
    if (env.METRICS && (ev.category === "wifi" || ev.category === "battery" || ev.category === "system")) {
      env.METRICS.writeDataPoint({
        indexes: [installationId],
        blobs: [appName, String(ev.origin_device_id ?? ""), ev.category, ev.kind],
        doubles: [
          occurredAt,
          Number(payload.rssi_dbm ?? payload.signal_pct ?? 0),
          Number(payload.level_pct ?? payload.cpu_pct ?? 0),
        ],
      });
    }

    accepted.push(ev.event_id);
  }

  for (let i = 0; i < statements.length; i += MAX_BATCH_STATEMENT_CHUNK) {
    await env.DB.batch(statements.slice(i, i + MAX_BATCH_STATEMENT_CHUNK));
  }

  return json({ accepted_ids: accepted, server_ms: nowMs });
}

async function handleBackupUpload(req: Request, env: Env, path: string): Promise<Response> {
  if (!env.BACKUPS) {
    return json({ error: "R2 BACKUPS bucket binding is not configured" }, 501);
  }
  if (env.CK_TELEMETRY_API_KEY) {
    const provided = req.headers.get("x-ck-api-key") ?? "";
    if (!timingSafeEqualStr(provided, env.CK_TELEMETRY_API_KEY)) {
      return json({ error: "invalid api key" }, 401);
    }
  }
  const parts = path.split("/").filter(Boolean); // ["v1", "backups", installationId, filename]
  if (parts.length < 4 || !req.body) {
    return json({ error: "expected PUT /v1/backups/:installation_id/:filename with body" }, 400);
  }
  const key = `backups/${parts[2]}/${parts.slice(3).join("_")}`;
  await env.BACKUPS.put(key, req.body);
  return json({ ok: true, key });
}

function checkAdminAuth(req: Request, url: URL, env: Env): Response | null {
  if (env.CK_TRUST_CF_ACCESS === "true" && req.headers.get("cf-access-authenticated-user-email")) {
    return null;
  }
  const expected = env.CK_ADMIN_TOKEN;
  if (!expected) {
    return json({ error: "CK_ADMIN_TOKEN is not configured on this worker" }, 503);
  }
  const bearer = (req.headers.get("authorization") || "").replace(/^Bearer\s+/i, "").trim();
  const queryTok = (url.searchParams.get("token") || "").trim();
  const candidate = bearer || queryTok;
  if (!candidate || !timingSafeEqualStr(candidate, expected)) {
    return json({ error: "admin authorization required" }, 401);
  }
  return null;
}

async function handleListInstallations(env: Env): Promise<Response> {
  const res = await env.DB.prepare(
    `SELECT
       i.installation_id,
       i.app_name,
       i.last_sender_id,
       i.last_seen_ms,
       i.created_at_ms,
       (SELECT COUNT(*) FROM form_submissions f WHERE f.installation_id = i.installation_id) AS total_submissions,
       (SELECT COUNT(*) FROM form_submissions f WHERE f.installation_id = i.installation_id AND f.status = 'played') AS played_rounds,
       (SELECT COUNT(*) FROM devices d WHERE d.installation_id = i.installation_id) AS device_count
     FROM installations i
     ORDER BY i.last_seen_ms DESC`
  ).all();
  return json({ installations: res.results ?? [] });
}

async function handleInstallationSummary(env: Env, installationId: string): Promise<Response> {
  const [prizesRes, devicesRes, alertsRes] = await env.DB.batch([
    env.DB.prepare(
      `SELECT
         CASE WHEN reward_title != '' THEN reward_title ELSE '(No Prize)' END AS reward_title,
         reward_id,
         COUNT(*) AS count,
         ROUND(AVG(score), 1) AS avg_score,
         MIN(score) AS min_score,
         MAX(score) AS max_score
       FROM form_submissions
       WHERE installation_id = ?1 AND status = 'played'
       GROUP BY reward_title, reward_id
       ORDER BY count DESC`
    ).bind(installationId),
    env.DB.prepare(
      `SELECT device_id, role, device_name, device_type, model, os_name, os_version, app_version, state_json, updated_at_ms
       FROM devices
       WHERE installation_id = ?1
       ORDER BY role DESC, updated_at_ms DESC`
    ).bind(installationId),
    env.DB.prepare(
      `SELECT event_id, origin_device_id, origin_role, category, kind, severity, occurred_at_ms, payload_json
       FROM events
       WHERE installation_id = ?1 AND (severity IN ('warn', 'error', 'critical') OR category = 'audit')
       ORDER BY occurred_at_ms DESC
       LIMIT 40`
    ).bind(installationId),
  ]);

  return json({
    installation_id: installationId,
    prizes: prizesRes.results ?? [],
    devices: devicesRes.results ?? [],
    recent_alerts: alertsRes.results ?? [],
  });
}

async function handleListSubmissions(env: Env, installationId: string, url: URL): Promise<Response> {
  const limit = Math.min(200, Math.max(1, Number(url.searchParams.get("limit") || 50)));
  const offset = Math.max(0, Number(url.searchParams.get("offset") || 0));
  const q = (url.searchParams.get("q") || "").trim();

  if (q) {
    const like = `%${q}%`;
    const res = await env.DB.prepare(
      `SELECT submission_id, form_id, source_device_id, source_device_name, round_id, status, score,
              reward_id, reward_title, fields_json, outcome_json, created_at_ms
       FROM form_submissions
       WHERE installation_id = ?1 AND (fields_json LIKE ?2 OR reward_title LIKE ?2)
       ORDER BY created_at_ms DESC
       LIMIT ?3 OFFSET ?4`
    )
      .bind(installationId, like, limit, offset)
      .all();
    return json({ items: res.results ?? [], limit, offset });
  }

  const res = await env.DB.prepare(
    `SELECT submission_id, form_id, source_device_id, source_device_name, round_id, status, score,
            reward_id, reward_title, fields_json, outcome_json, created_at_ms
     FROM form_submissions
     WHERE installation_id = ?1
     ORDER BY created_at_ms DESC
     LIMIT ?2 OFFSET ?3`
  )
    .bind(installationId, limit, offset)
    .all();
  return json({ items: res.results ?? [], limit, offset });
}

async function handleExportSubmissionsCSV(env: Env, installationId: string, url: URL): Promise<Response> {
  const formId = (url.searchParams.get("form_id") || "registration").trim();
  const schemaRow = await env.DB.prepare(
    "SELECT schema_json FROM form_schemas WHERE installation_id = ?1 AND form_id = ?2"
  )
    .bind(installationId, formId)
    .first<{ schema_json: string }>();

  let fieldKeys: string[] = [];
  if (schemaRow?.schema_json) {
    try {
      const parsed = JSON.parse(schemaRow.schema_json) as { fields?: Array<{ key: string }> };
      fieldKeys = (parsed.fields ?? []).map((f) => f.key);
    } catch {}
  }

  const rowsRes = await env.DB.prepare(
    `SELECT submission_id, created_at_ms, source_device_id, source_device_name, status,
            score, reward_id, reward_title, fields_json, outcome_json
     FROM form_submissions
     WHERE installation_id = ?1 AND form_id = ?2
     ORDER BY created_at_ms DESC
     LIMIT 100000`
  )
    .bind(installationId, formId)
    .all<{
      submission_id: string;
      created_at_ms: number;
      source_device_id: string;
      source_device_name: string;
      status: string;
      score: number | null;
      reward_id: string;
      reward_title: string;
      fields_json: string;
      outcome_json: string;
    }>();

  const rows = rowsRes.results ?? [];
  if (fieldKeys.length === 0 && rows.length > 0) {
    const keySet = new Set<string>();
    for (const r of rows.slice(0, 50)) {
      try {
        for (const k of Object.keys(JSON.parse(r.fields_json) as Record<string, unknown>)) keySet.add(k);
      } catch {}
    }
    fieldKeys = Array.from(keySet);
  }

  const header = [
    "submission_id",
    "created_at",
    "source_device_id",
    "source_device_name",
    "status",
    ...fieldKeys,
    "score",
    "reward_id",
    "reward_title",
  ];

  const lines: string[] = [header.map(csvCell).join(",")];
  for (const r of rows) {
    let fields: Record<string, unknown> = {};
    try {
      fields = JSON.parse(r.fields_json) as Record<string, unknown>;
    } catch {}
    const cols: string[] = [
      r.submission_id,
      new Date(r.created_at_ms).toISOString(),
      r.source_device_id,
      r.source_device_name,
      r.status,
      ...fieldKeys.map((k) => (fields[k] !== undefined && fields[k] !== null ? String(fields[k]) : "")),
      r.score !== null && r.score !== undefined ? String(r.score) : "",
      r.reward_id,
      r.reward_title,
    ];
    lines.push(cols.map(csvCell).join(","));
  }

  return new Response(lines.join("\r\n") + "\r\n", {
    status: 200,
    headers: {
      ...CORS_HEADERS,
      "Content-Type": "text/csv; charset=utf-8",
      "Content-Disposition": `attachment; filename="${installationId}-${formId}.csv"`,
    },
  });
}

// ---------------------------------------------------------------------------
// Crypto, Compression & Wire Helpers (100% Web Standard APIs)
// ---------------------------------------------------------------------------

async function readMaybeGzippedBody(req: Request, maxBytes: number): Promise<Uint8Array> {
  if (!req.body) return new Uint8Array(0);
  const isGzip = (req.headers.get("content-encoding") || "").toLowerCase().includes("gzip");
  const stream = isGzip ? req.body.pipeThrough(new DecompressionStream("gzip")) : req.body;
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    if (value) {
      total += value.byteLength;
      if (total > maxBytes) {
        await reader.cancel();
        throw new Error(`decompressed body exceeds ${maxBytes} bytes`);
      }
      chunks.push(value);
    }
  }

  const out = new Uint8Array(total);
  let offset = 0;
  for (const c of chunks) {
    out.set(c, offset);
    offset += c.byteLength;
  }
  return out;
}

function decodeBase64(b64: string): Uint8Array | null {
  try {
    const normalized = b64.replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized + "=".repeat((4 - (normalized.length % 4)) % 4);
    const bin = atob(padded);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

async function deriveDeviceId(pubBytes: Uint8Array): Promise<string> {
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", pubBytes));
  return Array.from(hash.slice(0, 8), (b) => b.toString(16).padStart(2, "0")).join("");
}

async function verifyEd25519(pubBytes: Uint8Array, message: Uint8Array, sigBytes: Uint8Array): Promise<boolean> {
  try {
    const key = await crypto.subtle.importKey("raw", pubBytes, { name: "Ed25519" }, false, ["verify"]);
    return await crypto.subtle.verify("Ed25519", key, sigBytes, message);
  } catch {
    return false;
  }
}

function timingSafeEqualStr(a: string, b: string): boolean {
  const enc = new TextEncoder();
  const ba = enc.encode(a);
  const bb = enc.encode(b);
  if (ba.byteLength !== bb.byteLength) return false;
  let diff = 0;
  for (let i = 0; i < ba.byteLength; i++) {
    diff |= ba[i] ^ bb[i];
  }
  return diff === 0;
}

function csvCell(val: string): string {
  if (/[",\r\n]/.test(val)) {
    return `"${val.replace(/"/g, '""')}"`;
  }
  return val;
}

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      ...CORS_HEADERS,
      "Content-Type": "application/json; charset=utf-8",
      "Cache-Control": "no-store",
    },
  });
}

const ADMIN_DASHBOARD_HTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>corekiosk Telemetry Console</title>
<style>
  :root {
    --ground: #F2F0EB; --surface: #FFFFFF; --recessed: #E8E5DE; --line: #D6D2C8;
    --ink: #161615; --ink-sec: #52504A; --accent: #D94E1F; --accent-soft: #FCEEE9;
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--ground); color: var(--ink); font: 15px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
  header { background: var(--surface); border-bottom: 1px solid var(--line); padding: 18px 28px; display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; }
  h1 { margin: 0; font-size: 20px; letter-spacing: -0.3px; }
  main { max-width: 1080px; margin: 28px auto; padding: 0 24px 64px; display: grid; gap: 24px; }
  .sheet { background: var(--surface); border: 1px solid var(--line); border-radius: 14px; overflow: hidden; }
  .bar { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  input, select { height: 42px; padding: 0 14px; border: 1px solid #B8B3A6; border-radius: 10px; background: var(--ground); font-size: 15px; color: var(--ink); }
  button, a.btn { height: 42px; padding: 0 18px; border: 0; border-radius: 10px; background: var(--ink); color: #FFF; font-weight: 600; font-size: 14px; cursor: pointer; text-decoration: none; display: inline-flex; align-items: center; }
  a.btn-accent { background: var(--accent); }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 14px 18px; border-bottom: 1px solid var(--line); }
  th { color: var(--ink-sec); font-weight: 600; font-size: 13px; background: #FAF9F6; }
  tr:last-child td { border-bottom: 0; }
  .mono { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 13px; }
  .tag { display: inline-block; padding: 3px 9px; border-radius: 6px; background: var(--accent-soft); color: var(--accent); font-weight: 700; font-size: 13px; }
  .sec-title { font-size: 18px; font-weight: 700; margin: 0 0 10px; }
</style>
</head>
<body>
<header>
  <h1>corekiosk Field Telemetry</h1>
  <div class="bar">
    <input id="tok" type="password" placeholder="Admin Token" />
    <button id="loadBtn">Load Installations</button>
    <select id="instSel" style="display:none"></select>
    <a id="csvBtn" class="btn btn-accent" style="display:none" target="_blank">Download CSV</a>
  </div>
</header>
<main>
  <section id="prizesSec" style="display:none">
    <h2 class="sec-title">Prize Redemptions</h2>
    <div class="sheet"><table id="prizesTbl"><thead><tr><th>Prize</th><th>Awarded</th><th>Avg Score</th><th>Range</th></tr></thead><tbody></tbody></table></div>
  </section>
  <section id="subsSec" style="display:none">
    <h2 class="sec-title">Recent Players</h2>
    <div class="sheet"><table id="subsTbl"><thead><tr><th>Time</th><th>Player</th><th>Status</th><th>Score</th><th>Prize</th></tr></thead><tbody></tbody></table></div>
  </section>
</main>
<script>
const tokIn = document.getElementById('tok');
const sel = document.getElementById('instSel');
const csvBtn = document.getElementById('csvBtn');
tokIn.value = sessionStorage.getItem('ck_admin_tok') || '';

async function api(path) {
  const t = tokIn.value.trim();
  sessionStorage.setItem('ck_admin_tok', t);
  const res = await fetch(path, { headers: t ? { Authorization: 'Bearer ' + t } : {} });
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
  return res.json();
}

async function loadInstallations() {
  try {
    const data = await api('/v1/admin/installations');
    sel.innerHTML = '';
    for (const it of data.installations) {
      const opt = document.createElement('option');
      opt.value = it.installation_id;
      opt.textContent = (it.app_name || it.installation_id) + ' (' + it.total_submissions + ' plays)';
      sel.appendChild(opt);
    }
    if (data.installations.length) {
      sel.style.display = '';
      await loadDetail(sel.value);
    }
  } catch (e) { alert(e.message); }
}

async function loadDetail(id) {
  csvBtn.style.display = '';
  csvBtn.href = '/v1/admin/installations/' + encodeURIComponent(id) + '/submissions.csv?token=' + encodeURIComponent(tokIn.value.trim());
  const [sum, subs] = await Promise.all([
    api('/v1/admin/installations/' + encodeURIComponent(id) + '/summary'),
    api('/v1/admin/installations/' + encodeURIComponent(id) + '/submissions?limit=50'),
  ]);
  document.getElementById('prizesSec').style.display = '';
  document.getElementById('subsSec').style.display = '';
  const pb = document.querySelector('#prizesTbl tbody');
  pb.innerHTML = sum.prizes.map(p => '<tr><td><strong>' + p.reward_title + '</strong></td><td class="mono">' + p.count + '</td><td class="mono">' + (p.avg_score ?? '-') + '</td><td class="mono">' + (p.min_score ?? '-') + ' – ' + (p.max_score ?? '-') + '</td></tr>').join('') || '<tr><td colspan="4">No finished rounds yet.</td></tr>';
  const sb = document.querySelector('#subsTbl tbody');
  sb.innerHTML = subs.items.map(s => {
    let f = {}; try { f = JSON.parse(s.fields_json); } catch {}
    const who = [f.name || s.submission_id, f.ig_username ? '@' + f.ig_username : '', f.email || ''].filter(Boolean).join(' · ');
    return '<tr><td class="mono">' + new Date(s.created_at_ms).toLocaleString() + '</td><td>' + who + '</td><td>' + s.status + '</td><td class="mono">' + (s.score ?? '-') + '</td><td>' + (s.reward_title ? '<span class="tag">' + s.reward_title + '</span>' : '-') + '</td></tr>';
  }).join('') || '<tr><td colspan="5">No submissions yet.</td></tr>';
}

document.getElementById('loadBtn').onclick = loadInstallations;
sel.onchange = () => loadDetail(sel.value);
</script>
</body>
</html>`;
