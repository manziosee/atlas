# Atlas Console

The web UI for [Atlas](../README.md): a public landing page and an operator
console for any Atlas cluster. It's a static Next.js site, so your browser
calls your node directly and no console server ever handles a token.

## Features

- **Landing page:** what Atlas is, how it works, features, quick start, docs
  and security notes, with live numbers from the read-only demo cluster.
- **Connect:** a node URL and an API token, or the read-only demo.
  - Explains invalid, expired and under-scoped tokens, and CORS problems.
  - Supports one-click links (`/connect#node=…&token=…`) from a node's
    dev-mode banner.
- **Overview:** node and service counts, health breakdown, regions, what
  needs attention, live activity and recent changes.
- **Nodes:** search and state filters. Admins can evict failed nodes.
- **Services:** search and filters, plus a detail drawer with health
  settings and change history.
  - With write scope: register, edit (`If-Match`), drain/undrain, heartbeat
    and deregister.
- **Activity:** the live event stream (pause, filter, reconnect) and the
  history log.
- **Lookup tester:** the node's real choice, and why each candidate was
  picked or skipped.
- **Usage & reliability (admin only):** privacy-limited daily unique-browser
  and page-view counts, per-node API requests/errors, and live node/service health.
- **Admin:** credential metadata, backup download, node eviction.

What you can do follows the token's scopes (`read`, `write`, `admin`). The
console detects them without changing anything; admin navigation is hidden
unless the connected token has admin scope, and admin API routes enforce that
scope server-side. Status always uses text and an icon as well as colour.
Pages work with a keyboard and on phones.

## Develop

```bash
npm ci
npm run dev        # http://localhost:3000
npm run typecheck
npm test           # unit tests
npm run build      # static export to out/
```

`/demo-api` proxies to the public demo cluster (`ATLAS_DEMO_ORIGIN` to
change it). `/analytics-api` proxies to a local Atlas API on port 8080
(`ATLAS_ANALYTICS_ORIGIN` to change it). Netlify proxies that path to the
hosted Atlas API.

The site stores a random browser ID in local storage to count daily unique
browsers. The API stores only a daily hash, normalized page totals and a
90-day history; it does not persist raw IDs, names, query strings, IP
addresses or device fingerprints. API counters are in-memory and reset when
their node restarts. Daily unique-browser totals use a mergeable approximation
across reachable nodes and are capped at 10,000 recorded visitors per node per
day. At most 256 page paths per node per day are retained; later new paths are
combined under `/other`. See the site's [terms](/terms/) for the full disclosure.

Live end-to-end tests run the console's API client against a real node:

```bash
ATLAS_LIVE_URL=http://127.0.0.1:8080 ATLAS_LIVE_TOKEN=<admin token> npm test
```

## Connecting a node

Browsers only let this page call a node that allows its origin:

```bash
ATLAS_CORS_ORIGINS=https://your-console.netlify.app   # dev mode allows any origin
ATLAS_CONSOLE_URL=https://your-console.netlify.app    # optional redirect from the node
```

## Deploy

Import the repository in Netlify. The repository's root `netlify.toml`
builds this folder, publishes `out/`, proxies the demo and sets security
headers. It also proxies `/analytics-api/*` to `https://atlas-edge.fly.dev`;
the node's existing Fly volume persists the analytics tables. The hosted
console opts into collection at build time and the Fly node opts into receiving
it. For other static hosts, serve `out/`, set
`NEXT_PUBLIC_ATLAS_ANALYTICS_ENABLED=true` to collect page views, and configure
the same proxy. Telemetry ingestion is disabled by default on Atlas nodes; set
`ATLAS_ANALYTICS_ENABLED=true` on each receiving node to opt in.

| Build-time variable | Default | Purpose |
|---------------------|---------|---------|
| `NEXT_PUBLIC_ATLAS_DEMO_URL` | `/demo-api` | Demo node URL (must allow your origin if not proxied) |
| `NEXT_PUBLIC_ATLAS_DEMO_TOKEN` | the public read-only demo token | Demo token |
| `NEXT_PUBLIC_ATLAS_ANALYTICS_ENABLED` | `false` | Enable hosted-console anonymous page-view collection |

## Layout

```
app/                 routes: landing (/), /connect, and the console group
  (console)/         dashboard, nodes, services, activity, lookup, admin
components/landing/  landing-page sections
components/console/  shell, shared UI, service form, toasts
lib/api.ts           typed Atlas API client and plain-language errors
lib/sse.ts           streaming Server-Sent Events parser
lib/connection.tsx   connection, token and scope state
lib/cluster.tsx      members, services and the live event stream
test/                Vitest unit tests and opt-in live tests
```
