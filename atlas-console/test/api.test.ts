import { afterEach, describe, expect, it, vi } from "vitest";
import { AtlasClient, AtlasError, explain } from "@/lib/api";

type Route = (url: string, init: RequestInit) => Response;
function mockFetch(route: Route) {
  const calls: { url: string; init: RequestInit }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => (calls.push({ url, init }), route(url, init))));
  return calls;
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const node = { node: { id: "n1", addr: "a:1", region: "eu", state: "alive", incarnation: 1 }, version: "v1", commit: "c" };

afterEach(() => vi.unstubAllGlobals());

describe("AtlasClient", () => {
  it("sends the bearer token and follows service pages", async () => {
    const calls = mockFetch((url) =>
      url.includes("cursor=next") ? json(200, { services: [{ id: "b" }] }) : json(200, { services: [{ id: "a" }], next_cursor: "next" }),
    );
    const services = await new AtlasClient("http://node:8080/", "tok").services();
    expect(services.map((s) => s.id)).toEqual(["a", "b"]);
    expect(calls[0].url).toBe("http://node:8080/v1/services?limit=1000");
    expect((calls[0].init.headers as Record<string, string>).Authorization).toBe("Bearer tok");
  });

  it("detects read, write and admin scopes without changing anything", async () => {
    const scopes = async (canAdmin: boolean, canWrite: boolean) => {
      const calls = mockFetch((url, init) => {
        if (url.endsWith("/v1/node")) return json(200, node);
        if (url.includes("/v1/admin/")) return canAdmin ? json(200, { credentials: [] }) : json(403, { code: "insufficient_scope", error: "credential lacks the admin scope" });
        if (init.method === "PATCH") return canWrite ? json(404, { code: "not_found", error: "service not found" }) : json(403, { code: "insufficient_scope", error: "x" });
        throw new Error(`unexpected ${init.method} ${url}`);
      });
      const result = await new AtlasClient("http://n", "t").detectScopes();
      expect(calls.every((c) => c.init.method !== "DELETE" && c.init.method !== "POST")).toBe(true);
      return result.scopes;
    };
    expect(await scopes(true, true)).toEqual(["read", "write", "admin"]);
    expect(await scopes(false, true)).toEqual(["read", "write"]);
    expect(await scopes(false, false)).toEqual(["read"]);
  });

  it("turns failures into plain-language errors", async () => {
    mockFetch(() => json(401, { code: "unauthorized", error: "a valid bearer token is required", request_id: "r1" }));
    const err = await new AtlasClient("http://n", "bad").node().catch((e) => e);
    expect(err).toBeInstanceOf(AtlasError);
    expect(err.requestId).toBe("r1");
    expect(explain(err)).toMatch(/didn't accept this token/);

    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    expect(explain(await new AtlasClient("http://n", "t").node().catch((e) => e))).toMatch(/ATLAS_CORS_ORIGINS/);

    expect(explain(new AtlasError(409, "owner_alive", "x", undefined, "n2"))).toMatch(/\(n2\)/);
  });

  it("sends If-Match with the revision on updates", async () => {
    const calls = mockFetch(() => json(200, { id: "s" }));
    await new AtlasClient("http://n", "t").update("s 1", { draining: true }, 7);
    expect(calls[0].url).toBe("http://n/v1/services/s%201");
    expect((calls[0].init.headers as Record<string, string>)["If-Match"]).toBe('"7"');
    expect(calls[0].init.body).toBe('{"draining":true}');
  });

  it("streams events, reporting ids and resets", async () => {
    const body = 'id: 4\ndata: {"id":"4","type":"join","kind":"member","at":"2026-01-01T00:00:00Z"}\n\nevent: reset\ndata: {"type":"reset"}\n\n';
    const calls = mockFetch(() => new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
    const seen: string[] = [];
    let opened = false;
    await new AtlasClient("http://n", "t").streamEvents({
      lastEventId: "3",
      signal: new AbortController().signal,
      onOpen: () => (opened = true),
      onEvent: (e, id) => seen.push(`${e.kind}:${id}`),
    });
    expect(opened).toBe(true);
    expect(seen).toEqual(["member:4", "reset:4"]);
    expect(calls[0].url).toBe("http://n/v1/events?last_event_id=3");
  });
});
