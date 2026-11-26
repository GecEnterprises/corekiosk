# `@corekiosk/telemetry-worker` (Cloudflare Workers + D1)

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/GecEnterprises/corekiosk/tree/main/cloudflare/telemetry-worker)

Zero-dependency serverless telemetry, player-lead, and prize-tally backend for `corekiosk` installations.

## 1-Click Deploy

Click **Deploy to Cloudflare** above to automatically provision the `corekiosk-telemetry` D1 database, prompt for `CK_TELEMETRY_API_KEY` and `CK_ADMIN_TOKEN` (from [`.dev.vars.example`](./.dev.vars.example)), apply [`migrations/0001_init.sql`](./migrations/0001_init.sql), and deploy the Worker.

## CLI Quick Deploy (3 Commands)

```powershell
cd cloudflare/telemetry-worker
npm install

# 1. Create the D1 SQLite database and paste its database_id into wrangler.toml
npx wrangler d1 create corekiosk-telemetry
npx wrangler d1 migrations apply corekiosk-telemetry --remote

# 2. Set ingest & admin secrets
npx wrangler secret put CK_TELEMETRY_API_KEY
npx wrangler secret put CK_ADMIN_TOKEN

# 3. Deploy to Cloudflare's global edge
npm run deploy
```

## Point Your Kiosk PC & Companion App at the Worker

- **Kiosk PC (`ckserver`):**
  ```powershell
  $env:CK_TELEMETRY_URL = "https://corekiosk-telemetry.<your-subdomain>.workers.dev/v1/ingest"
  $env:CK_TELEMETRY_API_KEY = "<your-ingest-key>"
  ```
- **Companion RN / Web (for cellular Tablet-as-Mule sync when the PC has no internet):**
  ```powershell
  $env:EXPO_PUBLIC_TELEMETRY_URL = "https://corekiosk-telemetry.<your-subdomain>.workers.dev/v1/ingest"
  $env:EXPO_PUBLIC_TELEMETRY_API_KEY = "<your-ingest-key>"
  ```

## Client & PM Reporting

Open `https://corekiosk-telemetry.<your-subdomain>.workers.dev/admin` and enter your `CK_ADMIN_TOKEN` to view live installations, prize redemption tallies, recent player runs, and one-click CSV downloads.
