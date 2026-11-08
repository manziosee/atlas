# Admin area

The site owner's private dashboard at `/admin`. It shows usage (visitors,
page views and countries), the systems using Atlas and where they are,
node versions, GitHub adoption, and the live health of the Atlas cluster
behind the site. It runs as Netlify Functions next to the static console.
Netlify Blobs (store `atlas-ops`) holds the owner's password hash, login
throttling and a short GitHub cache.

## How it is protected

- `/admin` is not linked anywhere and is excluded from search engines;
  everything behind it needs the owner's sign-in.
- There is one owner account. The first sign-in uses
  `ATLAS_OPS_INITIAL_PASSWORD` and must set a new password before anything
  else. The new password is stored as a scrypt hash; changing it signs out
  every other session.
- Sessions are HMAC-signed `__Host-` cookies (HttpOnly, Secure,
  SameSite=Strict) that last 12 hours. Requests that change something must
  come from the site's own origin.
- Five failed sign-ins lock an IP address out for 15 minutes; twenty lock
  the account.
- The Atlas admin token stays in the function environment. The browser only
  receives totals.

## Settings

Set these as secret environment variables on the Netlify site.

| Variable | Purpose |
|----------|---------|
| `ATLAS_OPS_EMAIL` | The owner's sign-in email. |
| `ATLAS_OPS_INITIAL_PASSWORD` | First sign-in only; replaced on first use. |
| `ATLAS_OPS_SESSION_SECRET` | 32+ random characters for signing sessions (`openssl rand -hex 32`). |
| `ATLAS_OPS_API_URL` | Atlas node the dashboard reads, e.g. `https://atlas-edge.fly.dev`. |
| `ATLAS_OPS_API_TOKEN` | An admin-scope Atlas token for that cluster. |
| `ATLAS_TELEMETRY_KEY` | Same value as the nodes' `ATLAS_TELEMETRY_KEY`; the site's `/telemetry/collect` relay sends it. |
| `ATLAS_OPS_GITHUB_REPO` | Optional, default `manziosee/atlas`. |

The Atlas nodes need `ATLAS_ANALYTICS_ENABLED=true` and the same
`ATLAS_TELEMETRY_KEY`. The site sends events only when it is built with
`NEXT_PUBLIC_ATLAS_ANALYTICS_ENABLED=true`, which `netlify.toml` sets for
the hosted site.

## Endpoints

| Route | Use |
|-------|-----|
| `GET /ops-api/session` | Whether this browser is signed in |
| `POST /ops-api/login` | Sign in |
| `POST /ops-api/password` | Change the password |
| `POST /ops-api/logout` | Sign out |
| `GET /ops-api/overview?days=` | Dashboard data (1–90 days) |
| `POST /telemetry/collect` | Public: anonymous site events, relayed with the visitor's country |

## Test

`test/ops.test.ts` covers sign-in, lockout, sessions, password changes,
dashboard data and the relay. `test/ops-live.test.ts` runs the same flow
against a real node when `OPS_LIVE_URL`, `OPS_LIVE_TOKEN` and
`OPS_LIVE_KEY` are set.
