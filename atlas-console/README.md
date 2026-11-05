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
- **Admin:** credential metadata, backup download, node eviction.

What you can do follows the token's scopes (`read`, `write`, `admin`). The
console detects them without changing anything. Status always uses text and
an icon as well as colour. Pages work with a keyboard and on phones.

## Develop

```bash
npm ci
npm run dev        # http://localhost:3000
npm run typecheck
npm test           # unit tests
npm run build      # static export to out/
```

`/demo-api` proxies to the public demo cluster (`ATLAS_DEMO_ORIGIN` to
change it), as `netlify.toml` does in production.

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
headers. For other static hosts, serve `out/`.

| Build-time variable | Default | Purpose |
|---------------------|---------|---------|
| `NEXT_PUBLIC_ATLAS_DEMO_URL` | `/demo-api` | Demo node URL (must allow your origin if not proxied) |
| `NEXT_PUBLIC_ATLAS_DEMO_TOKEN` | the public read-only demo token | Demo token |

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
