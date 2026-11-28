// =============================================================================
// corekiosk — Zero-Dependency Single-File Supabase Edge Function (`telemetry`)
//
// Deploy option A (100% in Browser — Zero CLI):
//   1. Supabase Dashboard -> Edge Functions -> Deploy a new function -> Via Editor
//   2. Function name: `telemetry`
//   3. Paste this file, click Deploy, then toggle "Enforce JWT Verification: OFF".
//      No API key, installation ID, or device ID is required; anonymous batches are
//      accepted. Ed25519 signatures are still verified when supplied.
//
// Deploy option B (Supabase CLI):
//   npx supabase functions deploy telemetry --no-verify-jwt
// =============================================================================

interface DenoEnv {
  get(key: string): string | undefined;
}
declare const Deno: {
  env: DenoEnv;
  serve: (handler: (req: Request) => Response | Promise<Response>) => void;
};

const CORS_HEADERS: Record<string, string> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
  "Access-Control-Allow-Headers":
    "Content-Type, Content-Encoding, Authorization, apikey, X-CK-API-Key, X-CK-Device-ID, X-CK-Installation-ID, X-CK-Timestamp, X-CK-Public-Key, X-CK-Signature",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      ...CORS_HEADERS,
      "Content-Type": "application/json; charset=utf-8",
      "Cache-Control": "no-store",
    },
  });
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64.trim());
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

function bytesToHex(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i++) {
    out += bytes[i].toString(16).padStart(2, "0");
  }
  return out;
}

async function deviceIdFromPublicKey(pubKeyBytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", pubKeyBytes.buffer as ArrayBuffer);
  return bytesToHex(new Uint8Array(digest).slice(0, 8));
}

async function verifyEd25519(
  pubKeyB64: string,
  rawPayload: Uint8Array,
  sigB64: string
): Promise<boolean> {
  try {
    const pubBytes = base64ToBytes(pubKeyB64);
    const sigBytes = base64ToBytes(sigB64);
    if (pubBytes.byteLength !== 32 || sigBytes.byteLength !== 64) return false;
    const key = await crypto.subtle.importKey(
      "raw",
      pubBytes.buffer as ArrayBuffer,
      { name: "Ed25519" },
      false,
      ["verify"]
    );
    return await crypto.subtle.verify(
      "Ed25519",
      key,
      sigBytes.buffer as ArrayBuffer,
      rawPayload.buffer as ArrayBuffer
    );
  } catch {
    return false;
  }
}

async function readRequestBytes(req: Request): Promise<Uint8Array> {
  if (!req.body) return new Uint8Array(0);
  const encoding = (req.headers.get("Content-Encoding") ?? "").toLowerCase();
  if (encoding.includes("gzip")) {
    const stream = req.body.pipeThrough(new DecompressionStream("gzip"));
    const buf = await new Response(stream).arrayBuffer();
    return new Uint8Array(buf);
  }
  return new Uint8Array(await req.arrayBuffer());
}

async function handleIngest(req: Request): Promise<Response> {
  // Deliberately no API-key/JWT auth. The endpoint accepts anonymous telemetry.
  let rawBytes: Uint8Array;
  try {
    rawBytes = await readRequestBytes(req);
  } catch (err) {
    return jsonResponse({ code: "bad_request", error: `decompress body: ${String(err)}` }, 400);
  }

  let batch: {
    installation_id?: string;
    app_name?: string;
    sender_device_id?: string;
    events?: Array<Record<string, unknown>>;
  };
  try {
    batch = JSON.parse(new TextDecoder().decode(rawBytes));
  } catch {
    return jsonResponse({ code: "bad_request", error: "invalid JSON batch payload" }, 400);
  }

  const senderId =
    (batch.sender_device_id || req.headers.get("X-CK-Device-ID") || "").trim() || "anonymous";
  const installationId =
    (batch.installation_id || req.headers.get("X-CK-Installation-ID") || "").trim() || senderId;
  if (!Array.isArray(batch.events)) {
    return jsonResponse({ code: "bad_request", error: "missing events[]" }, 400);
  }

  // Verify self-certifying Ed25519 signature when X-CK-Signature / X-CK-Public-Key are provided
  const sigB64 = (req.headers.get("X-CK-Signature") ?? "").trim();
  let pubKeyB64 = (req.headers.get("X-CK-Public-Key") ?? "").trim();

  if (pubKeyB64 !== "" && senderId !== "anonymous") {
    try {
      const derivedId = await deviceIdFromPublicKey(base64ToBytes(pubKeyB64));
      if (derivedId !== senderId) {
        return jsonResponse(
          { code: "unauthorized", error: "X-CK-Public-Key fingerprint does not match sender_device_id" },
          401
        );
      }
    } catch {
      return jsonResponse({ code: "unauthorized", error: "malformed X-CK-Public-Key" }, 401);
    }
  }

  if (sigB64 !== "" && pubKeyB64 !== "") {
    const valid = await verifyEd25519(pubKeyB64, rawBytes, sigB64);
    if (!valid) {
      return jsonResponse({ code: "unauthorized", error: "invalid Ed25519 batch signature" }, 401);
    }
  }

  // Call atomic Postgres RPC `public.ck_ingest_batch` via Supabase's local PostgREST
  const supabaseUrl = (Deno.env.get("SUPABASE_URL") ?? "").replace(/\/+$/, "");
  const serviceRoleKey = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "";
  if (!supabaseUrl || !serviceRoleKey) {
    return jsonResponse(
      { code: "internal", error: "SUPABASE_URL or SUPABASE_SERVICE_ROLE_KEY missing in Edge Function env" },
      500
    );
  }

  const rpcRes = await fetch(`${supabaseUrl}/rest/v1/rpc/ck_ingest_batch`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      apikey: serviceRoleKey,
      Authorization: `Bearer ${serviceRoleKey}`,
    },
    body: JSON.stringify({
      p_batch: {
        ...batch,
        installation_id: installationId,
        sender_device_id: senderId,
      },
      // Anonymous batches must not pin a TOFU key shared by every anonymous sender.
      p_sender_public_key: senderId === "anonymous" ? "" : pubKeyB64,
    }),
  });

  if (!rpcRes.ok) {
    const errText = await rpcRes.text();
    return jsonResponse(
      { code: "store_error", error: `Supabase RPC ck_ingest_batch failed (${rpcRes.status}): ${errText}` },
      500
    );
  }

  const result = (await rpcRes.json()) as {
    ok?: boolean;
    status?: number;
    error?: string;
    accepted_ids?: string[];
    server_ms?: number;
  };
  if (result.ok === false) {
    return jsonResponse({ code: "rejected", error: result.error ?? "ingest rejected" }, result.status ?? 400);
  }

  return jsonResponse({
    accepted_ids: result.accepted_ids ?? [],
    server_ms: result.server_ms ?? Date.now(),
  });
}

Deno.serve(async (req: Request): Promise<Response> => {
  if (req.method === "OPTIONS") {
    return new Response(null, { status: 204, headers: CORS_HEADERS });
  }

  const url = new URL(req.url);
  if (req.method === "GET") {
    return jsonResponse({
      ok: true,
      service: "corekiosk-telemetry-supabase",
      endpoint: `${url.origin}/functions/v1/telemetry`,
      hint: "POST gzipped corekiosk batches to this URL; auth and installation/device IDs are optional",
    });
  }

  if (req.method === "POST") {
    return await handleIngest(req);
  }

  return jsonResponse({ code: "not_found", error: "method not supported" }, 405);
});
