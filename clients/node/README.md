# Atlas client for Node.js and TypeScript

A typed client for the [Atlas](https://github.com/manziosee/atlas) service
registry, with no runtime dependencies (Node.js 20+, built on `fetch`). It
covers the whole HTTP API: registration with idempotency keys, heartbeats,
revision-checked updates, draining, routed lookups, metadata filters,
history, resumable event streams and admin calls.

```bash
npm install @manziosee/atlas-client
```

```ts
import { AtlasClient, AtlasError } from "@manziosee/atlas-client";

const atlas = new AtlasClient("https://atlas.example:8080", process.env.ATLAS_TOKEN!);

const me = await atlas.register(
  { name: "payments", address: "10.0.0.7:8443", ttl_seconds: 15, metadata: { team: "payments" } },
  { idempotencyKey: "payments-host-7" },
);
await atlas.heartbeat(me.id);

const instance = await atlas.lookup("payments", { region: "eu", metadata: { team: "payments" } });

try {
  await atlas.update(me.id, { version: "2.0.0" }, me.revision);
} catch (err) {
  if (err instanceof AtlasError && err.code === "precondition_failed") {
    // someone changed it first; re-read and retry
  }
}

for await (const event of atlas.watch({ kinds: ["service"], service: "payments" })) {
  console.log(event.type, event.service?.address);
}
```

Licensed under Apache-2.0.
