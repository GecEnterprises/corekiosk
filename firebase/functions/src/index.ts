import { onRequest } from "firebase-functions/v2/https";
import { initializeApp } from "firebase-admin/app";
import { getFirestore, FieldValue } from "firebase-admin/firestore";
import * as crypto from "node:crypto";
import * as zlib from "node:zlib";

initializeApp();
const db = getFirestore();

interface TelemetryEvent {
  event_id: string;
  origin_device_id: string;
  origin_role: string;
  origin_boot_id: string;
  origin_seq: number;
  category: string;
  kind: string;
  severity: string;
  occurred_at_ms: number;
  payload: Record<string, unknown>;
  relayed_from?: string;
}

interface BatchEnvelope {
  installation_id: string;
  app_name: string;
  sender_device_id: string;
  sent_at_ms: number;
  events: TelemetryEvent[];
}

/**
 * Thin idempotent ingest endpoint for corekiosk telemetry & CKTelemetryTransmit forms.
 * - Routes `form.submission` into table-shaped `forms/{formId}/rows/{submissionId}` (create-only).
 * - Routes `board.entry_added` / `board.entry_removed` as 2P-Set CRDT (tombstone wins).
 * - Updates `devices/{deviceId}` using monotonic (origin_boot_id, origin_seq) LWW.
 * - Writes discrete alerts/events with create-only semantics (ALREADY_EXISTS => accepted).
 */
export const ingestBatch = onRequest({ maxInstances: 20 }, async (req, res) => {
  if (req.method !== "POST") {
    res.status(405).json({ error: "method not allowed" });
    return;
  }

  const expectedKey = process.env.CK_TELEMETRY_API_KEY;
  if (expectedKey && req.header("x-ck-api-key") !== expectedKey) {
    res.status(401).json({ error: "unauthorized" });
    return;
  }

  let rawBytes: Buffer;
  try {
    const raw = req.rawBody ?? Buffer.from(JSON.stringify(req.body ?? {}));
    rawBytes = req.header("content-encoding") === "gzip" ? zlib.gunzipSync(raw) : raw;
  } catch (err) {
    res.status(400).json({ error: `invalid body encoding: ${String(err)}` });
    return;
  }

  let env: BatchEnvelope;
  try {
    env = JSON.parse(rawBytes.toString("utf8")) as BatchEnvelope;
  } catch {
    res.status(400).json({ error: "invalid json payload" });
    return;
  }

  if (!env.installation_id || !Array.isArray(env.events)) {
    res.status(400).json({ error: "installation_id and events are required" });
    return;
  }

  const orgId = process.env.CK_ORG_ID || "default";
  const installRef = db.doc(`orgs/${orgId}/installations/${env.installation_id}`);

  // Optional Ed25519 signature verification if the installation has registered a public key.
  const sigB64 = req.header("x-ck-signature");
  const senderDeviceId = req.header("x-ck-device-id") || env.sender_device_id;
  if (sigB64 && senderDeviceId) {
    const devDoc = await installRef.collection("devices").doc(senderDeviceId).get();
    const pubB64 = devDoc.get("public_key") as string | undefined;
    if (pubB64) {
      const ok = verifyEd25519Raw(Buffer.from(pubB64, "base64"), rawBytes, Buffer.from(sigB64, "base64"));
      if (!ok) {
        res.status(401).json({ error: "invalid ed25519 signature" });
        return;
      }
    }
  }

  const nowMs = Date.now();
  await installRef.set(
    {
      app_name: env.app_name || "",
      last_seen_ms: nowMs,
      updated_at: FieldValue.serverTimestamp(),
    },
    { merge: true }
  );

  const accepted: string[] = [];

  for (const ev of env.events) {
    if (!ev.event_id || !ev.kind) continue;

    try {
      if (ev.kind === "form.submission") {
        await writeFormRow(installRef, ev, nowMs);
      } else if (ev.kind === "form.outcome") {
        await mergeFormOutcome(installRef, ev);
      } else if (ev.kind === "device.profile" || ev.kind === "device.status") {
        await updateDeviceMonotonic(installRef, ev, nowMs);
      } else if (ev.kind === "board.entry_added") {
        await upsertBoardEntry(installRef, ev, false);
      } else if (ev.kind === "board.entry_removed") {
        await upsertBoardEntry(installRef, ev, true);
      } else if (ev.kind === "telemetry.chunk") {
        await createIdempotent(installRef.collection("telemetry_chunks").doc(ev.event_id), {
          ...ev,
          received_at_ms: nowMs,
        });
      } else {
        await createIdempotent(installRef.collection("alerts").doc(ev.event_id), {
          ...ev,
          received_at_ms: nowMs,
        });
      }
      accepted.push(ev.event_id);
    } catch (err) {
      // Log and leave unaccepted so shipper retries safely.
      console.error("ingest event failed", ev.event_id, err);
    }
  }

  res.status(200).json({ accepted_ids: accepted, server_ms: nowMs });
});

async function createIdempotent(
  ref: FirebaseFirestore.DocumentReference,
  data: Record<string, unknown>
): Promise<void> {
  try {
    await ref.create(data);
  } catch (err: unknown) {
    const code = (err as { code?: number | string })?.code;
    if (code === 6 || code === "already-exists") {
      return; // Idempotent duplicate from peer relay -> treat as success.
    }
    throw err;
  }
}

async function writeFormRow(
  installRef: FirebaseFirestore.DocumentReference,
  ev: TelemetryEvent,
  nowMs: number
): Promise<void> {
  const p = ev.payload || {};
  const formId = String(p.form_id || "registration");
  const submissionId = String(p.submission_id || ev.event_id);
  const fields = (p.fields as Record<string, unknown>) || {};

  const formRef = installRef.collection("forms").doc(formId);
  if (p.form_schema) {
    await formRef.set(
      {
        form_id: formId,
        schema: p.form_schema,
        updated_at_ms: nowMs,
      },
      { merge: true }
    );
  }

  // Table-shaped row: user fields live directly at top-level alongside underscored system columns.
  await createIdempotent(formRef.collection("rows").doc(submissionId), {
    ...fields,
    _submission_id: submissionId,
    _form_id: formId,
    _form_version: p.form_version ?? 1,
    _source_device_id: p.source_device_id ?? ev.origin_device_id,
    _source_device_name: p.source_device_name ?? "",
    _round_id: p.round_id ?? null,
    _status: p.status ?? "submitted",
    _created_at_ms: ev.occurred_at_ms,
    _synced_at_ms: nowMs,
  });
}

async function mergeFormOutcome(
  installRef: FirebaseFirestore.DocumentReference,
  ev: TelemetryEvent
): Promise<void> {
  const p = ev.payload || {};
  const formId = String(p.form_id || "registration");
  const submissionId = String(p.submission_id || "");
  if (!submissionId) return;
  await installRef
    .collection("forms")
    .doc(formId)
    .collection("rows")
    .doc(submissionId)
    .set(
      {
        _status: p.status ?? "finished",
        _outcome: p.outcome ?? null,
      },
      { merge: true }
    );
}

async function updateDeviceMonotonic(
  installRef: FirebaseFirestore.DocumentReference,
  ev: TelemetryEvent,
  nowMs: number
): Promise<void> {
  const deviceId = String(ev.payload?.device_id || ev.origin_device_id);
  const ref = installRef.collection("devices").doc(deviceId);
  await db.runTransaction(async (tx) => {
    const snap = await tx.get(ref);
    if (snap.exists) {
      const prevTs = Number(snap.get("origin_occurred_at_ms") || 0);
      const prevSeq = Number(snap.get("origin_seq") || 0);
      const prevBoot = String(snap.get("origin_boot_id") || "");
      if (prevBoot === ev.origin_boot_id && ev.origin_seq <= prevSeq) {
        return; // Drop stale out-of-order relayed status.
      }
      if (prevBoot !== ev.origin_boot_id && ev.occurred_at_ms < prevTs) {
        return;
      }
    }
    tx.set(
      ref,
      {
        ...ev.payload,
        device_id: deviceId,
        origin_boot_id: ev.origin_boot_id,
        origin_seq: ev.origin_seq,
        origin_occurred_at_ms: ev.occurred_at_ms,
        last_synced_at_ms: nowMs,
      },
      { merge: true }
    );
  });
}

async function upsertBoardEntry(
  installRef: FirebaseFirestore.DocumentReference,
  ev: TelemetryEvent,
  isRemoval: boolean
): Promise<void> {
  const p = ev.payload || {};
  const boardId = String(p.board_id || "default");
  const entryId = String(p.entry_id || ev.event_id);
  const ref = installRef.collection("boards").doc(boardId).collection("entries").doc(entryId);
  if (isRemoval) {
    // 2P-Set CRDT: tombstone always wins regardless of arrival order.
    await ref.set(
      {
        board_id: boardId,
        entry_id: entryId,
        removed_at_ms: p.removed_at_ms ?? ev.occurred_at_ms,
        removed_by_operator_id: p.removed_by_operator_id ?? "",
      },
      { merge: true }
    );
    return;
  }
  await createIdempotent(ref, {
    board_id: boardId,
    entry_id: entryId,
    submission_id: p.submission_id ?? null,
    sort_value: p.sort_value ?? 0,
    display: p.display ?? {},
    created_at_ms: p.created_at_ms ?? ev.occurred_at_ms,
    removed_at_ms: null,
  });
}

function verifyEd25519Raw(rawPub32: Buffer, message: Buffer, sig: Buffer): boolean {
  if (rawPub32.length !== 32 || sig.length !== 64) return false;
  // DER SPKI prefix for 32-byte raw Ed25519 public key.
  const spkiPrefix = Buffer.from("302a300506032b6570032100", "hex");
  const key = crypto.createPublicKey({
    key: Buffer.concat([spkiPrefix, rawPub32]),
    format: "der",
    type: "spki",
  });
  return crypto.verify(null, message, key, sig);
}
