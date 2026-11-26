import assert from "node:assert/strict";
import { webcrypto } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import worker, {
  type D1DatabaseLike,
  type D1PreparedStatementLike,
  type D1ResultLike,
  type Env,
} from "../src/index.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const MIGRATION_SQL = readFileSync(join(__dirname, "..", "..", "migrations", "0001_init.sql"), "utf8");

/** Lightweight Node SQLite adapter matching Cloudflare D1's prepare/bind/batch semantics. */
function createInMemoryD1(): D1DatabaseLike {
  const sqlite = new DatabaseSync(":memory:");
  sqlite.exec(MIGRATION_SQL);

  const makeStmt = (query: string, bound: unknown[] = []): D1PreparedStatementLike => {
    // Convert D1 positional ?1, ?2... parameters to standard ? in order for node:sqlite
    const runPrepared = () => {
      const expandedArgs: unknown[] = [];
      const normalizedSql = query.replace(/\?(\d+)/g, (_, numStr: string) => {
        const idx = Number(numStr) - 1;
        const v = bound[idx];
        expandedArgs.push(v === undefined ? null : v);
        return "?";
      });
      const stmt = sqlite.prepare(normalizedSql);
      const isSelect = /^\s*SELECT\b/i.test(normalizedSql);
      if (isSelect) {
        const rows = stmt.all(...(expandedArgs as never[])) as Record<string, unknown>[];
        return { results: rows, success: true };
      }
      stmt.run(...(expandedArgs as never[]));
      return { results: [], success: true };
    };

    return {
      bind(...values: unknown[]) {
        return makeStmt(query, values);
      },
      async first<T = Record<string, unknown>>(colName?: string): Promise<T | null> {
        const res = runPrepared();
        const row = res.results?.[0];
        if (!row) return null;
        if (colName) return (row[colName] as T) ?? null;
        return row as T;
      },
      async all<T = Record<string, unknown>>(): Promise<D1ResultLike<T>> {
        return runPrepared() as D1ResultLike<T>;
      },
      async run(): Promise<D1ResultLike> {
        return runPrepared();
      },
    };
  };

  return {
    prepare(query: string) {
      return makeStmt(query);
    },
    async batch<T = unknown>(statements: D1PreparedStatementLike[]): Promise<D1ResultLike<T>[]> {
      sqlite.exec("BEGIN IMMEDIATE");
      try {
        const out: D1ResultLike<T>[] = [];
        for (const s of statements) {
          out.push((await s.all()) as D1ResultLike<T>);
        }
        sqlite.exec("COMMIT");
        return out;
      } catch (err) {
        sqlite.exec("ROLLBACK");
        throw err;
      }
    },
  };
}

function toBase64(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("base64");
}

async function generateDeviceIdentity() {
  const kp = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"])) as webcrypto.CryptoKeyPair;
  const pubRaw = new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey));
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", pubRaw));
  const deviceId = Array.from(hash.slice(0, 8), (b) => b.toString(16).padStart(2, "0")).join("");
  return { kp, pubB64: toBase64(pubRaw), deviceId };
}

async function gzipBytes(raw: Uint8Array): Promise<Uint8Array> {
  const cs = new CompressionStream("gzip");
  const writer = cs.writable.getWriter();
  void writer.write(raw);
  void writer.close();
  const out = new Response(cs.readable);
  return new Uint8Array(await out.arrayBuffer());
}

describe("corekiosk Cloudflare Telemetry Worker", () => {
  it("verifies Ed25519 gzipped batches, merges out-of-order outcomes, deduplicates relays, and exports CSV", async () => {
    const db = createInMemoryD1();
    const env: Env = {
      DB: db,
      CK_TELEMETRY_API_KEY: "secret-ingest-key",
      CK_ADMIN_TOKEN: "secret-admin-tok",
    };

    const server = await generateDeviceIdentity();
    const tablet = await generateDeviceIdentity();

    // 1. Tablet Mule uploads form.outcome BEFORE PC uploads form.submission (out-of-order arrival!)
    const tabletBatch = {
      installation_id: server.deviceId,
      app_name: "joyland-essential",
      sender_device_id: tablet.deviceId,
      sent_at_ms: 1700000010000,
      events: [
        {
          event_id: "evt_outcome_1",
          origin_device_id: server.deviceId,
          origin_role: "server",
          origin_boot_id: "boot1",
          origin_seq: 2,
          category: "form",
          kind: "form.outcome",
          severity: "info",
          occurred_at_ms: 1700000009000,
          relayed_from: server.deviceId,
          payload: {
            form_id: "registration",
            submission_id: "sub_001",
            round_id: "rnd_001",
            status: "played",
            outcome: {
              score: 340,
              reward_id: "raincoat",
              reward_title: "Rain Coat",
            },
          },
        },
      ],
    };

    const tabletRaw = new TextEncoder().encode(JSON.stringify(tabletBatch));
    const tabletSig = new Uint8Array(await crypto.subtle.sign("Ed25519", tablet.kp.privateKey, tabletRaw));

    const res1 = await worker.fetch(
      new Request("https://telemetry.example.com/v1/ingest", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CK-API-Key": "secret-ingest-key",
          "X-CK-Device-ID": tablet.deviceId,
          "X-CK-Public-Key": tablet.pubB64,
          "X-CK-Signature": toBase64(tabletSig),
        },
        body: tabletRaw,
      }),
      env
    );
    assert.equal(res1.status, 200);
    const body1 = (await res1.json()) as { accepted_ids: string[] };
    assert.deepEqual(body1.accepted_ids, ["evt_outcome_1"]);

    // 2. Reject tampered batch with invalid Ed25519 signature -> 401
    const tamperedRaw = new TextEncoder().encode(JSON.stringify({ ...tabletBatch, app_name: "forged" }));
    const resForged = await worker.fetch(
      new Request("https://telemetry.example.com/v1/ingest", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CK-API-Key": "secret-ingest-key",
          "X-CK-Device-ID": tablet.deviceId,
          "X-CK-Signature": toBase64(tabletSig),
        },
        body: tamperedRaw,
      }),
      env
    );
    assert.equal(resForged.status, 401);

    // 3. Kiosk PC comes online later and uploads gzipped batch containing form.submission + duplicate outcome
    const pcBatch = {
      installation_id: server.deviceId,
      app_name: "joyland-essential",
      sender_device_id: server.deviceId,
      sent_at_ms: 1700000020000,
      events: [
        {
          event_id: "sub_001",
          origin_device_id: tablet.deviceId,
          origin_role: "tablet",
          origin_boot_id: "boot1",
          origin_seq: 1,
          category: "form",
          kind: "form.submission",
          severity: "info",
          occurred_at_ms: 1700000001000,
          payload: {
            submission_id: "sub_001",
            form_id: "registration",
            form_version: 1,
            form_schema: {
              id: "registration",
              fields: [
                { key: "name", label: "Name" },
                { key: "ig_username", label: "Instagram" },
                { key: "email", label: "Email" },
              ],
            },
            source_device_id: tablet.deviceId,
            source_device_name: "Booth Tablet",
            round_id: "rnd_001",
            status: "submitted",
            fields: {
              name: "Regan",
              ig_username: "regan",
              email: "regan@example.com",
            },
          },
        },
        // Duplicate of evt_outcome_1 already uploaded by Tablet Mule
        tabletBatch.events[0],
      ],
    };

    const pcRaw = new TextEncoder().encode(JSON.stringify(pcBatch));
    const pcSig = new Uint8Array(await crypto.subtle.sign("Ed25519", server.kp.privateKey, pcRaw));
    const pcGz = await gzipBytes(pcRaw);

    const res2 = await worker.fetch(
      new Request("https://telemetry.example.com/v1/ingest", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Content-Encoding": "gzip",
          "X-CK-API-Key": "secret-ingest-key",
          "X-CK-Device-ID": server.deviceId,
          "X-CK-Public-Key": server.pubB64,
          "X-CK-Signature": toBase64(pcSig),
        },
        body: pcGz,
      }),
      env
    );
    assert.equal(res2.status, 200);

    // 4. Verify Admin Summary & CSV Export merged both fields AND outcome cleanly with zero duplicates!
    const sumRes = await worker.fetch(
      new Request(`https://telemetry.example.com/v1/admin/installations/${server.deviceId}/summary`, {
        headers: { Authorization: "Bearer secret-admin-tok" },
      }),
      env
    );
    assert.equal(sumRes.status, 200);
    const sumJson = (await sumRes.json()) as {
      prizes: Array<{ reward_title: string; count: number; avg_score: number }>;
    };
    assert.equal(sumJson.prizes.length, 1);
    assert.equal(sumJson.prizes[0].reward_title, "Rain Coat");
    assert.equal(sumJson.prizes[0].count, 1);
    assert.equal(sumJson.prizes[0].avg_score, 340);

    const csvRes = await worker.fetch(
      new Request(`https://telemetry.example.com/v1/admin/installations/${server.deviceId}/submissions.csv`, {
        headers: { Authorization: "Bearer secret-admin-tok" },
      }),
      env
    );
    assert.equal(csvRes.status, 200);
    const csvText = await csvRes.text();
    assert.match(csvText, /submission_id,created_at,source_device_id,source_device_name,status,name,ig_username,email,score,reward_id,reward_title/);
    assert.match(csvText, /sub_001,.*,Booth Tablet,played,Regan,regan,regan@example\.com,340,raincoat,Rain Coat/);
  });
});
