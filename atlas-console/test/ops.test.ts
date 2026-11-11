import { describe, expect, it, vi } from "vitest";
import {
  SESSION_COOKIE,
  createSession,
  handleCollect,
  handleLogin,
  handleLogout,
  handleOverview,
  handlePassword,
  handleSession,
  hashPassword,
  loadAccount,
  passwordProblem,
  readEnv,
  readSession,
  verifyPassword,
  type Deps,
  type KV,
  type OpsRequest,
} from "@/netlify/ops/core";

const SITE = "https://atlas.example";
const ENV = {
  ATLAS_OPS_EMAIL: "Owner@Example.com",
  ATLAS_OPS_INITIAL_PASSWORD: "Initial@123",
  ATLAS_OPS_SESSION_SECRET: "s".repeat(48),
  ATLAS_OPS_API_URL: "https://node.example/",
  ATLAS_OPS_API_TOKEN: "admin-token-admin-token-admin-token",
  ATLAS_TELEMETRY_KEY: "telemetry-key-telemetry-key-telemetry",
  URL: SITE,
};

function memoryKV(): KV & { data: Map<string, unknown> } {
  const data = new Map<string, unknown>();
  return {
    data,
    get: async <T>(k: string) => (data.has(k) ? (structuredClone(data.get(k)) as T) : null),
    set: async (k, v) => void data.set(k, structuredClone(v)),
    delete: async (k) => void data.delete(k),
  };
}

function request(method: string, path: string, opts: { body?: unknown; cookie?: string; headers?: Record<string, string>; ip?: string; country?: string } = {}): OpsRequest {
  const headers = new Headers({ origin: SITE, "x-atlas-ops": "1", ...opts.headers });
  if (opts.cookie) headers.set("cookie", `${SESSION_COOKIE}=${opts.cookie}`);
  return { method, url: `${SITE}${path}`, headers, json: async () => opts.body ?? {}, ip: opts.ip ?? "203.0.113.7", country: opts.country };
}

function cookieFrom(res: Response) {
  const set = res.headers.get("set-cookie") ?? "";
  expect(set).toContain("HttpOnly");
  expect(set).toContain("Secure");
  expect(set).toContain("SameSite=Strict");
  return set.split(";")[0].split("=").slice(1).join("=");
}

const deps = (kv = memoryKV(), extra: Partial<Deps> = {}): Deps => ({ kv, env: ENV, ...extra });

describe("configuration and passwords", () => {
  it("reports what is missing", () => {
    expect(readEnv({}).missing).toContain("ATLAS_OPS_API_TOKEN");
    expect(readEnv({ ...ENV, ATLAS_OPS_SESSION_SECRET: "short" }).missing.join()).toContain("32 characters");
    expect(readEnv(ENV).env?.email).toBe("owner@example.com");
  });

  it("hashes with scrypt and rejects weak passwords", async () => {
    const hash = await hashPassword("Correct horse 9!");
    expect(hash.startsWith("scrypt$")).toBe(true);
    expect(await verifyPassword("Correct horse 9!", hash)).toBe(true);
    expect(await verifyPassword("correct horse 9!", hash)).toBe(false);
    const env = readEnv(ENV).env!;
    expect(passwordProblem("short", env)).toMatch(/12/);
    expect(passwordProblem("alllowercaseletters", env)).toBeNull(); // 16+ characters
    expect(passwordProblem("lowercase123", env)).toMatch(/three/);
    expect(passwordProblem("Owner-Strong-99!", env)).toMatch(/email/);
    expect(passwordProblem(ENV.ATLAS_OPS_INITIAL_PASSWORD, env)).toMatch(/12|different/);
    expect(passwordProblem("Tr0ub4dour&3x!", env)).toBeNull();
  });

  it("rejects tampered and expired sessions", async () => {
    const env = readEnv(ENV).env!;
    const account = await loadAccount(memoryKV(), env);
    const token = createSession(account, env, 1_000_000);
    expect(readSession(token, account, env, 1_000_000)).not.toBeNull();
    expect(readSession(token.slice(0, -2) + "xx", account, env, 1_000_000)).toBeNull();
    expect(readSession(token, account, env, 1_000_000 + 13 * 3600 * 1000)).toBeNull();
    expect(readSession(token, { ...account, version: 2 }, env, 1_000_000)).toBeNull();
  });
});

describe("login, password change and sessions", () => {
  it("forces a password change, then revokes old sessions", async () => {
    const d = deps();
    // Not signed in.
    expect(await (await handleSession(request("GET", "/ops-api/session"), d)).json()).toEqual({ authenticated: false });

    // Initial password works (email is case-insensitive) but must be changed.
    let res = await handleLogin(request("POST", "/ops-api/login", { body: { email: "OWNER@example.com", password: ENV.ATLAS_OPS_INITIAL_PASSWORD } }), d);
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ authenticated: true, mustChange: true });
    const first = cookieFrom(res);
    expect((await handleOverview(request("GET", "/ops-api/overview", { cookie: first }), d)).status).toBe(403);

    // Weak or wrong-current changes are refused.
    res = await handlePassword(request("POST", "/ops-api/password", { cookie: first, body: { current: ENV.ATLAS_OPS_INITIAL_PASSWORD, next: "weak" } }), d);
    expect(res.status).toBe(400);
    res = await handlePassword(request("POST", "/ops-api/password", { cookie: first, body: { current: "nope", next: "Tr0ub4dour&3x!" } }), d);
    expect(res.status).toBe(400);

    res = await handlePassword(request("POST", "/ops-api/password", { cookie: first, body: { current: ENV.ATLAS_OPS_INITIAL_PASSWORD, next: "Tr0ub4dour&3x!" } }), d);
    expect(res.status).toBe(200);
    const second = cookieFrom(res);
    expect(await (await handleSession(request("GET", "/ops-api/session", { cookie: first }), d)).json()).toEqual({ authenticated: false });
    expect(await (await handleSession(request("GET", "/ops-api/session", { cookie: second }), d)).json()).toMatchObject({ authenticated: true, mustChange: false });

    // The initial password no longer works; the new one does.
    expect((await handleLogin(request("POST", "/ops-api/login", { body: { email: ENV.ATLAS_OPS_EMAIL, password: ENV.ATLAS_OPS_INITIAL_PASSWORD }, ip: "198.51.100.1" }), d)).status).toBe(401);
    expect((await handleLogin(request("POST", "/ops-api/login", { body: { email: ENV.ATLAS_OPS_EMAIL, password: "Tr0ub4dour&3x!" } }), d)).status).toBe(200);
    const stored = [...(d.kv as ReturnType<typeof memoryKV>).data.values()].map((v) => JSON.stringify(v)).join();
    expect(stored).not.toContain("Tr0ub4dour");

    res = await handleLogout(request("POST", "/ops-api/logout", { cookie: second }), d);
    expect(res.headers.get("set-cookie")).toContain("Max-Age=0");
  });

  it("locks out an address after repeated failures", async () => {
    const d = deps();
    for (let i = 0; i < 5; i++) {
      expect((await handleLogin(request("POST", "/ops-api/login", { body: { email: ENV.ATLAS_OPS_EMAIL, password: `wrong-${i}` } }), d)).status).toBe(401);
    }
    const res = await handleLogin(request("POST", "/ops-api/login", { body: { email: ENV.ATLAS_OPS_EMAIL, password: ENV.ATLAS_OPS_INITIAL_PASSWORD } }), d);
    expect(res.status).toBe(429);
    expect(Number(res.headers.get("retry-after"))).toBeGreaterThan(0);
    // Another address is not blocked.
    expect((await handleLogin(request("POST", "/ops-api/login", { ip: "192.0.2.9", body: { email: ENV.ATLAS_OPS_EMAIL, password: ENV.ATLAS_OPS_INITIAL_PASSWORD } }), d)).status).toBe(200);
  });

  it("refuses cross-site posts and unconfigured deployments", async () => {
    const d = deps();
    const body = { email: ENV.ATLAS_OPS_EMAIL, password: ENV.ATLAS_OPS_INITIAL_PASSWORD };
    expect((await handleLogin(request("POST", "/ops-api/login", { body, headers: { origin: "https://evil.example" } }), d)).status).toBe(403);
    const noHeader = request("POST", "/ops-api/login", { body });
    noHeader.headers.delete("x-atlas-ops");
    expect((await handleLogin(noHeader, d)).status).toBe(403);
    expect((await handleSession(request("GET", "/ops-api/session"), { kv: memoryKV(), env: {} })).status).toBe(503);
  });
});

describe("dashboard data", () => {
  it("combines Atlas and GitHub data with the token kept server-side", async () => {
    const d = deps();
    const kv = d.kv as ReturnType<typeof memoryKV>;
    const env = readEnv(ENV).env!;
    const account = { ...(await loadAccount(kv, env)), hash: await hashPassword("Tr0ub4dour&3x!"), mustChange: false };
    await kv.set("account", account);
    const cookie = createSession(account, env);

    const seen: { url: string; auth: string | null }[] = [];
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      seen.push({ url, auth: headers.get("authorization") });
      const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 });
      if (url.includes("/v1/admin/analytics")) return json({ daily: [{ date: "2026-11-08", page_views: 4 }], countries: [{ country: "RW", page_views: 4, visitors: 2 }] });
      if (url.endsWith("/v1/node")) return json({ node: { id: "n1" }, version: "v1" });
      if (url.endsWith("/v1/members")) return json({ members: [{ id: "n1", state: "alive", region: "cdg", addr: "a" }] });
      if (url.includes("/v1/services")) {
        return json({
          services: [
            { id: "a", name: "api", node_id: "n1", address: "x:1", region: "cdg", health: "healthy", updated_at: "" },
            { id: "b", name: "db", node_id: "n1", address: "x:2", region: "cdg", health: "unhealthy", updated_at: "" },
          ],
        });
      }
      if (url.includes("/v1/topology")) return new Response(JSON.stringify({ error: "boom" }), { status: 500, statusText: "Internal Server Error" });
      if (url.endsWith("/v1/admin/credentials")) {
        const soon = new Date(Date.now() + 2 * 24 * 3600 * 1000).toISOString();
        return json({
          credentials: [
            { id: "c1", name: "ci", scopes: ["write"], source: "managed", expires_at: soon, expired: false },
            { id: "c2", scopes: ["admin"], source: "static", expired: false },
            { id: "c3", name: "old", scopes: ["read"], source: "managed", expired: false, revoked: true },
          ],
        });
      }
      if (url.includes("/v1/admin/audit")) return json({ entries: [{ seq: 1, at: "2026-11-11T00:00:00Z", actor_id: "abc", action: "credential.create", resource_id: "c1" }] });
      if (url.endsWith("/repos/manziosee/atlas")) return json({ stargazers_count: 12, forks_count: 3, subscribers_count: 2, open_issues_count: 1, pushed_at: "2026-11-08T00:00:00Z" });
      if (url.includes("/releases")) return json([{ assets: [{ download_count: 5 }, { download_count: 2 }] }]);
      throw new Error(`unexpected ${url}`);
    });

    const res = await handleOverview(request("GET", "/ops-api/overview?days=7", { cookie }), { ...d, fetch: fetchMock as unknown as typeof fetch });
    expect(res.status).toBe(200);
    const data = await res.json();
    expect(data.services).toMatchObject({ total: 2, names: 2, healthy: 1, unhealthy: 1, problems: [{ id: "b", health: "unhealthy" }] });
    expect(data.analytics.countries[0]).toEqual({ country: "RW", page_views: 4, visitors: 2 });
    expect(data.github).toMatchObject({ stars: 12, releaseDownloads: 7 });
    expect(data.security.credentials).toMatchObject({ total: 3, active: 2, revoked: 1, managed: 2, expiringSoon: [{ id: "c1", name: "ci" }] });
    expect(data.security.audit[0]).toMatchObject({ action: "credential.create" });
    expect(seen.some((c) => c.url === "https://node.example/v1/admin/audit?limit=20")).toBe(true);
    expect(data.errors.topology).toContain("500");
    expect(JSON.stringify(data)).not.toContain(ENV.ATLAS_OPS_API_TOKEN);
    // Atlas calls carry the token; GitHub calls never do.
    for (const call of seen) {
      if (call.url.startsWith("https://node.example")) expect(call.auth).toBe(`Bearer ${ENV.ATLAS_OPS_API_TOKEN}`);
      else expect(call.auth).toBeNull();
    }
    expect(seen.some((c) => c.url === "https://node.example/v1/admin/analytics?days=7")).toBe(true);
    expect((await handleOverview(request("GET", "/ops-api/overview"), d)).status).toBe(401);
    expect((await handleOverview(request("GET", "/ops-api/overview?days=500", { cookie }), d)).status).toBe(400);
  });
});

describe("telemetry relay", () => {
  it("adds the country and key, and drops anything invalid", async () => {
    const calls: { url: string; init: RequestInit }[] = [];
    const fetchMock = vi.fn(async (url: string, init: RequestInit) => (calls.push({ url, init }), new Response(null, { status: 204 })));
    const d = deps(memoryKV(), { fetch: fetchMock as unknown as typeof fetch });
    const visitor = "123e4567-e89b-12d3-a456-426614174000";

    expect((await handleCollect(request("POST", "/telemetry/collect", { body: { visitor_id: visitor, path: "/docs/x" }, country: "RW" }), d)).status).toBe(204);
    expect((await handleCollect(request("POST", "/telemetry/collect", { body: { type: "connect", visitor_id: visitor, kind: "node", version: "v1.2.0" } }), d)).status).toBe(204);
    expect((await handleCollect(request("POST", "/telemetry/collect", { body: { visitor_id: "nope" } }), d)).status).toBe(204);
    expect((await handleCollect(request("POST", "/telemetry/collect", { body: { type: "click", visitor_id: visitor } }), d)).status).toBe(204);

    expect(calls).toHaveLength(2);
    expect(calls[0].url).toBe("https://node.example/v1/telemetry/event");
    expect(new Headers(calls[0].init.headers).get("x-atlas-telemetry-key")).toBe(ENV.ATLAS_TELEMETRY_KEY);
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ type: "pageview", visitor_id: visitor, path: "/docs/x", country: "RW" });
    expect(JSON.parse(String(calls[1].init.body))).toEqual({ type: "connect", visitor_id: visitor, kind: "node", version: "v1.2.0" });

    // Without a key the relay forwards nothing.
    const off = { ...d, env: { ...ENV, ATLAS_TELEMETRY_KEY: "" } };
    await handleCollect(request("POST", "/telemetry/collect", { body: { visitor_id: visitor } }), off);
    expect(calls).toHaveLength(2);
  });
});
