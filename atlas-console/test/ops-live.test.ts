// The ops backend against a real Atlas node. Skipped unless OPS_LIVE_URL,
// OPS_LIVE_TOKEN (admin) and OPS_LIVE_KEY (the node's ATLAS_TELEMETRY_KEY) are set.
import { describe, expect, it } from "vitest";
import { SESSION_COOKIE, handleCollect, handleLogin, handleOverview, handlePassword, type KV, type OpsRequest } from "@/netlify/ops/core";

const url = process.env.OPS_LIVE_URL;
const token = process.env.OPS_LIVE_TOKEN;
const key = process.env.OPS_LIVE_KEY;

describe.skipIf(!url || !token || !key)("ops backend against a live node", () => {
  const data = new Map<string, unknown>();
  const kv: KV = { get: async (k) => (data.get(k) ?? null) as never, set: async (k, v) => void data.set(k, v), delete: async (k) => void data.delete(k) };
  const env = {
    ATLAS_OPS_EMAIL: "owner@example.com",
    ATLAS_OPS_INITIAL_PASSWORD: "Initial@123",
    ATLAS_OPS_SESSION_SECRET: "x".repeat(48),
    ATLAS_OPS_API_URL: url,
    ATLAS_OPS_API_TOKEN: token,
    ATLAS_TELEMETRY_KEY: key,
    URL: "https://site.example",
  };
  const req = (method: string, path: string, body?: unknown, cookie?: string, country?: string): OpsRequest => ({
    method,
    url: `https://site.example${path}`,
    headers: new Headers({ origin: "https://site.example", "x-atlas-ops": "1", ...(cookie ? { cookie: `${SESSION_COOKIE}=${cookie}` } : {}) }),
    json: async () => body,
    ip: "203.0.113.50",
    country,
  });
  const cookie = (res: Response) => (res.headers.get("set-cookie") ?? "").split(";")[0].split("=").slice(1).join("=");

  it("relays telemetry, signs in, changes the password and loads the dashboard", async () => {
    const visitor = crypto.randomUUID();
    for (const [body, country] of [
      [{ visitor_id: visitor, path: "/docs/kubernetes" }, "RW"],
      [{ visitor_id: visitor, path: "/" }, "RW"],
      [{ type: "connect", visitor_id: visitor, kind: "node", version: "v9.9.9" }, "RW"],
      [{ visitor_id: crypto.randomUUID(), path: "/about" }, "KE"],
    ] as const) {
      expect((await handleCollect(req("POST", "/telemetry/collect", body, undefined, country), { kv, env })).status).toBe(204);
    }

    let res = await handleLogin(req("POST", "/ops-api/login", { email: env.ATLAS_OPS_EMAIL, password: env.ATLAS_OPS_INITIAL_PASSWORD }), { kv, env });
    expect(res.status).toBe(200);
    res = await handlePassword(req("POST", "/ops-api/password", { current: env.ATLAS_OPS_INITIAL_PASSWORD, next: "Live-Test-Pass-42!" }, cookie(res)), { kv, env });
    expect(res.status).toBe(200);
    res = await handleOverview(req("GET", "/ops-api/overview?days=1", undefined, cookie(res)), { kv, env });
    expect(res.status).toBe(200);
    const overview = await res.json();

    const today = overview.analytics.daily[0];
    expect(today.page_views).toBeGreaterThanOrEqual(3);
    expect(today.systems).toBeGreaterThanOrEqual(1);
    const countries = Object.fromEntries(overview.analytics.countries.map((c: { country: string; page_views: number }) => [c.country, c.page_views]));
    expect(countries.RW).toBeGreaterThanOrEqual(2);
    expect(countries.KE).toBeGreaterThanOrEqual(1);
    expect(overview.analytics.versions.some((v: { version: string }) => v.version === "v9.9.9")).toBe(true);
    expect(overview.members.length).toBeGreaterThanOrEqual(1);
    expect(overview.node.node.id).toBeTruthy();
    expect(overview.errors.analytics).toBeUndefined();
    expect(JSON.stringify(overview)).not.toContain(token);
  }, 60_000);
});
