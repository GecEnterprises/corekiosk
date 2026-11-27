// corekiosk — Platform-Agnostic Updates & Signed Release Manifest Edge Function
// Deploy with --no-verify-jwt (authenticates kiosks via X-CK-API-Key and publishers via X-CK-Publish-Key).

import { createClient } from "https://esm.sh/@supabase/supabase-js@2";

const corsHeaders = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
  "Access-Control-Allow-Headers": "Content-Type, X-CK-API-Key, X-CK-Publish-Key",
};

Deno.serve(async (req: Request) => {
  if (req.method === "OPTIONS") {
    return new Response(null, { status: 204, headers: corsHeaders });
  }

  const supabase = createClient(
    Deno.env.get("SUPABASE_URL") ?? "",
    Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") ?? "",
  );

  if (req.method === "GET") {
    const apiKey = Deno.env.get("CK_TELEMETRY_API_KEY") ?? "";
    if (apiKey && req.headers.get("X-CK-API-Key") !== apiKey) {
      return jsonResponse({ error: "unauthorized" }, 401);
    }

    const u = new URL(req.url);
    const app = (u.searchParams.get("app") ?? "").trim();
    let channel = (u.searchParams.get("channel") ?? "stable").trim();
    const installationId = (u.searchParams.get("installation_id") ?? "").trim();

    if (!app) {
      return jsonResponse({ error: "app query parameter is required" }, 400);
    }

    let pinnedVersion = "";
    if (installationId) {
      const { data: pin } = await supabase
        .from("installation_channels")
        .select("channel, pinned_version")
        .eq("installation_id", installationId)
        .maybeSingle();
      if (pin?.channel) channel = pin.channel;
      if (pin?.pinned_version) pinnedVersion = pin.pinned_version;
    }

    let query = supabase
      .from("releases")
      .select("*")
      .eq("app_name", app)
      .eq("channel", channel)
      .eq("is_active", true)
      .order("published_at_ms", { ascending: false });

    if (pinnedVersion) {
      query = query.eq("version", pinnedVersion);
    }

    const { data: rows, error } = await query;
    if (error) {
      return jsonResponse({ error: error.message }, 500);
    }

    const bucket = Deno.env.get("CK_RELEASES_BUCKET") ?? "ck-releases";
    const targets: Record<string, unknown> = {};
    for (const row of rows ?? []) {
      if (targets[row.target]) continue; // newest first per target
      let downloadUrl = row.artifact_url;
      if (!downloadUrl && row.storage_path) {
        const { data: signed } = await supabase.storage
          .from(bucket)
          .createSignedUrl(row.storage_path, 300);
        downloadUrl = signed?.signedUrl ?? "";
      }
      if (!downloadUrl) continue;
      targets[row.target] = {
        target: row.target,
        version: row.version,
        policy: row.policy,
        url: downloadUrl,
        storage_path: row.storage_path,
        sha256: row.sha256,
        size_bytes: row.size_bytes,
        key_id: row.key_id,
        sig: row.sig,
        published_at_ms: row.published_at_ms,
        notes: row.notes,
      };
    }

    return jsonResponse({ app, channel, targets }, 200);
  }

  if (req.method === "POST") {
    const pubKey = Deno.env.get("CK_PUBLISH_API_KEY") ?? "";
    if (pubKey && req.headers.get("X-CK-Publish-Key") !== pubKey) {
      return jsonResponse({ error: "invalid publish key" }, 401);
    }

    const body = await req.json();
    const app = String(body.app ?? "").trim();
    const channel = String(body.channel ?? "stable").trim();
    const art = body.artifact;
    if (!app || !art?.target || !art?.version || !art?.sha256 || !art?.sig) {
      return jsonResponse({ error: "app and signed artifact are required" }, 400);
    }

    const { error } = await supabase.from("releases").upsert({
      app_name: app,
      channel,
      target: art.target,
      version: art.version,
      policy: art.policy ?? "when_idle",
      storage_path: art.storage_path ?? "",
      artifact_url: art.url ?? "",
      sha256: art.sha256,
      size_bytes: art.size_bytes ?? 0,
      key_id: art.key_id ?? "",
      sig: art.sig,
      notes: art.notes ?? "",
      is_active: true,
      published_at_ms: art.published_at_ms ?? Date.now(),
    });

    if (error) {
      return jsonResponse({ error: error.message }, 500);
    }
    return jsonResponse({ ok: true, app, channel, target: art.target, version: art.version }, 200);
  }

  return jsonResponse({ error: "method not allowed" }, 405);
});

function jsonResponse(data: unknown, status: number): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { ...corsHeaders, "Content-Type": "application/json" },
  });
}
