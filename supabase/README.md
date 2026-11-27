# `corekiosk` Supabase Telemetry Backend (PostgreSQL + Edge Function)

Zero-dependency **Supabase** backend for `corekiosk` telemetry, player lead capture, and prize redemption tallies.

---

## Option A — 100% Browser Deployment (Zero CLI / Zero Config Files)

You can deploy the entire backend in the **Supabase Web Dashboard** in ~60 seconds without opening a terminal:

### Step 1: Create Tables, Views & Atomic Ingest RPC
1. Open your project in the [Supabase Dashboard](https://supabase.com/dashboard) → **SQL Editor** → **New query**.
2. Paste the contents of [`migrations/20250301000000_corekiosk_telemetry.sql`](./migrations/20250301000000_corekiosk_telemetry.sql) and click **Run**.
   - Creates `installations`, `devices`, `form_schemas`, `form_submissions`, `board_entries`, and `events`.
   - Creates convenience views `player_leads_view` (flattened player fields + score + prize won) and `prize_tallies_view` (count + average score per prize tier) right in your Supabase **Table Editor**.
   - Creates the atomic Postgres function `public.ck_ingest_batch`.

### Step 2: Deploy the Edge Function in the Browser
1. In the Supabase Dashboard, go to **Edge Functions** → **Deploy a new function** → **Via Web Editor**.
2. Set **Function name** to `telemetry`.
3. Paste the contents of [`functions/telemetry/index.ts`](./functions/telemetry/index.ts) and click **Deploy function**.
4. Open the newly deployed `telemetry` function → **Details / Settings**:
   - Toggle **Enforce JWT Verification** to **OFF** (so `ckserver` and Companion tablets authenticate using `X-CK-API-Key` + Ed25519 device signatures instead of a Supabase user JWT).
5. *(Optional)* In **Edge Functions → Secrets**, add `CK_TELEMETRY_API_KEY` with your chosen shared secret.

### Step 3: Paste Your URL into `cklauncher` & Click **Test Connection**
1. Copy your Edge Function URL from the Supabase Dashboard:
   ```text
   https://<your-project-ref>.supabase.co/functions/v1/telemetry
   ```
2. Open **`cklauncher`** → **Telemetry** tab → paste the URL into **Ingest URL** (and your `CK_TELEMETRY_API_KEY` if configured) → click **Test Connection**.
   - You will immediately see `OK — HTTP 200 (<latency>ms, 1 event accepted)` and a `telemetry.test_ping` row inside your Supabase **Table Editor → `events`** table!

---

## Option B — Supabase CLI Deployment

If you prefer deploying from a terminal:

```powershell
# 1. Link your Supabase project
npx supabase login
npx supabase link --project-ref <your-project-ref>

# 2. Push the PostgreSQL migration
npx supabase db push

# 3. (Optional) Set a shared ingest API key
npx supabase secrets set CK_TELEMETRY_API_KEY=<your-secret-key>

# 4. Deploy the Edge Function with verify_jwt disabled
npx supabase functions deploy telemetry --no-verify-jwt
```

---

## Viewing Leads, Prize Tallies & Exporting CSV in Supabase

Open **Table Editor** in the Supabase Dashboard:
- **`player_leads_view`** (or **`form_submissions`**): Every player registration automatically merged with their game score and resolved prize (`reward_title`), with 1-click **Export to CSV** in the top bar.
- **`prize_tallies_view`**: Live breakdown of how many of each prize tier (`Rain Coat`, `T-Shirt`, `Sling Bag`, `Scarf`) have been awarded and their average score.
- **`devices`**: Live hardware status, battery %, temperature, and Wi-Fi RSSI of the Kiosk PC and paired Companion tablets.
