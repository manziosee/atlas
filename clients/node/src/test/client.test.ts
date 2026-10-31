// Integration tests against a running Atlas node. Set ATLAS_TEST_ENDPOINT and
// an admin ATLAS_TEST_TOKEN to run them; CI starts a node for this. Without
// those variables the live tests are skipped.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { describe, it } from "node:test";

import { AtlasClient, AtlasError, parseEvents } from "../index.js";

const endpoint = process.env.ATLAS_TEST_ENDPOINT ?? "";
const token = process.env.ATLAS_TEST_TOKEN ?? "";
const live = { skip: !endpoint || !token ? "set ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN" : false };

const unique = (prefix: string): string => `${prefix}-${randomUUID().slice(0, 8)}`;

async function rejectsWithCode(promise: Promise<unknown>, code: string): Promise<AtlasError> {
  try {
    await promise;
  } catch (error) {
    assert.ok(error instanceof AtlasError, `expected AtlasError, got ${String(error)}`);
    assert.equal(error.code, code);
    return error;
  }
  assert.fail(`expected an AtlasError with code ${code}`);
}

describe("AtlasClient against a live node", live, () => {
  const atlas = new AtlasClient(endpoint || "http://127.0.0.1:1", token);

  it("reads the node and its members", async () => {
    const node = await atlas.node();
    assert.ok(node.node.id);
    assert.ok((await atlas.members()).some((member) => member.id === node.node.id));
  });

  it("registers idempotently, updates with If-Match, and deletes", async () => {
    const name = unique("ts");
    const first = await atlas.register({ name, address: "10.8.0.1:80", tags: ["ts"] }, { idempotencyKey: `deploy-${name}` });
    const again = await atlas.register({ name, address: "10.8.0.1:80", tags: ["ts"] }, { idempotencyKey: `deploy-${name}` });
    assert.equal(again.id, first.id);
    assert.equal(again.revision, 1);

    const updated = await atlas.update(first.id, { version: "2" }, 1);
    assert.equal(updated.version, "2");
    assert.equal(updated.address, "10.8.0.1:80");
    const stale = await rejectsWithCode(atlas.update(first.id, { version: "3" }, 1), "precondition_failed");
    assert.equal(stale.status, 412);

    const replaced = await atlas.replace(first.id, { name, address: "10.8.0.2:80" }, updated.revision);
    assert.equal(replaced.address, "10.8.0.2:80");
    assert.equal((await atlas.lookup(name)).id, first.id);

    await atlas.delete(first.id);
    const missing = await rejectsWithCode(atlas.get(first.id), "not_found");
    assert.ok(missing.requestId);
  });

  it("keeps TTL services healthy with heartbeats", async () => {
    const service = await atlas.register({ name: unique("ttl"), address: "10.8.1.1:80", ttl_seconds: 30 });
    assert.equal(service.health, "unknown");
    assert.equal((await atlas.heartbeat(service.id)).health, "healthy");
    const plain = await atlas.register({ name: unique("plain"), address: "10.8.1.2:80" });
    await rejectsWithCode(atlas.heartbeat(plain.id), "no_ttl");
  });

  it("pages listings and applies filters", async () => {
    const name = unique("page");
    for (let i = 0; i < 5; i++) {
      await atlas.register({ name, address: `10.8.2.${i + 1}:80`, tags: [i % 2 ? "odd" : "even"] });
    }
    const page = await atlas.listPage({ name }, "", 2);
    assert.equal(page.items.length, 2);
    assert.ok(page.next);
    assert.equal((await atlas.listServices({ name })).length, 5);
    assert.equal((await atlas.listServices({ name, tags: ["even"] })).length, 3);
  });

  it("drains instances out of lookups and applies routing overrides", async () => {
    const name = unique("drain");
    const blue = await atlas.register({ name, address: "10.8.3.1:80" });
    const green = await atlas.register({ name, address: "10.8.3.2:80" });
    assert.equal((await atlas.drain(blue.id)).draining, true);
    for (let i = 0; i < 4; i++) assert.equal((await atlas.lookup(name, { stale: "exclude", latency: "off" })).id, green.id);
    assert.equal((await atlas.undrain(blue.id)).draining, undefined);
    await rejectsWithCode(atlas.lookup(name, { stale: "sometimes" as "include" }), "validation_failed");
  });

  it("reads service history newest first", async () => {
    const name = unique("hist");
    const service = await atlas.register({ name, address: "10.8.4.1:80" });
    await atlas.update(service.id, { version: "2" });
    const first = await atlas.history(service.id, { limit: 1 });
    assert.equal(first.items[0]?.change, "updated");
    assert.equal(typeof first.nextBefore, "number");
    const rest = await atlas.history(service.id, { before: first.nextBefore });
    assert.deepEqual(rest.items.map((entry) => entry.change), ["registered"]);
    assert.equal((await atlas.recentHistory({ name })).items.length, 2);
  });

  it("streams service events and resets on an unknown event ID", async () => {
    const name = unique("watch");
    const controller = new AbortController();
    const received = (async () => {
      for await (const event of atlas.watch({ kinds: ["service"], service: name, signal: controller.signal })) {
        return event;
      }
      return undefined;
    })();
    await new Promise((resolve) => setTimeout(resolve, 500));
    await atlas.register({ name, address: "10.8.5.1:80" });
    const event = await received;
    controller.abort();
    assert.equal(event?.type, "service.registered");
    assert.equal(event?.service?.name, name);
    assert.ok(event?.id);

    for await (const reset of atlas.watch({ lastEventId: "elsewhere.1", reconnect: false })) {
      assert.equal(reset.type, "reset");
      break;
    }
  });

  it("lists credentials and downloads a backup", async () => {
    assert.ok((await atlas.credentials()).some((credential) => credential.current === true));
    const snapshot = await atlas.backup();
    assert.equal(new TextDecoder().decode(snapshot.slice(0, 15)), "SQLite format 3");
    await rejectsWithCode(atlas.evictNode("no-such-node"), "not_found");
  });
});

describe("AtlasClient offline", () => {
  it("rejects a non-origin endpoint", () => {
    assert.throws(() => new AtlasClient("ftp://example", "t"), TypeError);
    assert.throws(() => new AtlasClient("https://example/api", "t"), TypeError);
  });

  it("reports an unreachable node without a status", async () => {
    const error = await new AtlasClient("http://127.0.0.1:9", "t", { timeoutMs: 500 }).node().catch((e: unknown) => e);
    assert.ok(error instanceof AtlasError);
    assert.equal(error.status, undefined);
  });

  it("parses Server-Sent Events across chunk boundaries", async () => {
    const frames = ": connected\nretry: 3000\n\nid: e.1\nevent: join\ndata: {\"type\":\"join\",\"member\":{\"id\":\"n1\"}}\n\nevent: reset\ndata: {\"type\":\"reset\"}\n\n";
    const bytes = new TextEncoder().encode(frames);
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        for (let i = 0; i < bytes.length; i += 7) controller.enqueue(bytes.slice(i, i + 7));
        controller.close();
      },
    });
    const events = [];
    for await (const event of parseEvents(stream)) events.push(event);
    assert.deepEqual(events.map((e) => e.type), ["join", "reset"]);
    assert.equal(events[0]?.member?.id, "n1");
  });
});
