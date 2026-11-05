// End-to-end checks of the console's API client against a real node.
// Skipped unless ATLAS_LIVE_URL and ATLAS_LIVE_TOKEN (admin) are set:
//   ATLAS_LIVE_URL=http://127.0.0.1:8080 ATLAS_LIVE_TOKEN=... npm test
import { describe, expect, it } from "vitest";
import { AtlasClient, AtlasError, explain, type AtlasEvent } from "@/lib/api";

const url = process.env.ATLAS_LIVE_URL;
const token = process.env.ATLAS_LIVE_TOKEN;

describe.skipIf(!url || !token)("live node", () => {
  const client = new AtlasClient(url ?? "", token ?? "");
  const id = `console-e2e-${Date.now()}`;

  it("runs a full service lifecycle the way the console does", async () => {
    expect((await client.detectScopes()).scopes).toEqual(["read", "write", "admin"]);

    const controller = new AbortController();
    const events: AtlasEvent[] = [];
    let opened!: () => void;
    const open = new Promise<void>((r) => (opened = r));
    const stream = client.streamEvents({ signal: controller.signal, onOpen: opened, onEvent: (e) => events.push(e) }).catch(() => {});
    await open;

    const created = await client.register({ id, name: "console-e2e", address: "127.0.0.1:9", ttl_seconds: 30, tags: ["e2e"] });
    expect(created.health).toBe("unknown");
    expect((await client.heartbeat(id)).health).toBe("healthy");
    expect((await client.lookup("console-e2e", { tags: ["e2e"] })).id).toBe(id);

    const drained = await client.setDraining(id, true);
    expect(drained.draining).toBe(true);
    const lookupErr = await client.lookup("console-e2e").catch((e) => e);
    expect(lookupErr).toBeInstanceOf(AtlasError);
    expect(lookupErr.code).toBe("no_healthy_instance");

    // Stale revision is refused with a clear message.
    const stale = await client.update(id, { version: "2" }, created.revision).catch((e) => e);
    expect(stale.code).toBe("precondition_failed");
    expect(explain(stale)).toMatch(/changed this service/);

    // Switching from TTL to a health check clears the TTL.
    const switched = await client.update(id, { ttl_seconds: 0, health_check: "tcp://127.0.0.1:9", check_interval_seconds: 0 }, drained.revision);
    expect(switched.ttl_seconds ?? 0).toBe(0);
    expect(switched.health_check).toBe("tcp://127.0.0.1:9");

    const history = await client.history(id);
    expect(history.entries.map((e) => e.change)).toContain("registered");
    expect((await client.backup()).size).toBeGreaterThan(0);
    expect((await client.credentials()).some((c) => c.current)).toBe(true);

    await client.deregister(id);
    expect((await client.service(id).catch((e) => e)).code).toBe("not_found");

    await new Promise((r) => setTimeout(r, 300));
    controller.abort();
    await stream;
    const types = events.filter((e) => e.service?.id === id).map((e) => e.type);
    expect(types).toContain("service.registered");
    expect(types).toContain("service.deregistered");
  }, 30_000);
});
