// Private operations backend: one owner account, signed sessions and the data
// behind the ops dashboard. It runs as Netlify Functions; everything that
// touches the platform (Blobs, request context) is passed in, so this module
// is plain TypeScript and unit-tested.
//
// Secrets live only in environment variables and the Blobs store. The Atlas
// admin token never leaves the server: the browser gets a session cookie and
// aggregated numbers, nothing else.

import { createHash, createHmac, randomBytes, scrypt as scryptCb, timingSafeEqual, type ScryptOptions } from "node:crypto";

export interface KV {
  get<T>(key: string): Promise<T | null>;
  set(key: string, value: unknown): Promise<void>;
  delete(key: string): Promise<void>;
}

export interface OpsEnv {
  email: string;
  initialPassword: string;
  sessionSecret: string;
  apiUrl: string;
  apiToken: string;
  telemetryKey: string;
  siteUrl: string;
  githubRepo: string;
}

export type EnvSource = Record<string, string | undefined>;

/** Reads the configuration; missing values make the ops area unavailable. */
export function readEnv(env: EnvSource): { env?: OpsEnv; missing: string[] } {
  const pick = {
    email: env.ATLAS_OPS_EMAIL,
    initialPassword: env.ATLAS_OPS_INITIAL_PASSWORD,
    sessionSecret: env.ATLAS_OPS_SESSION_SECRET,
    apiUrl: env.ATLAS_OPS_API_URL,
    apiToken: env.ATLAS_OPS_API_TOKEN,
  };
  const missing = Object.entries({
    ATLAS_OPS_EMAIL: pick.email,
    ATLAS_OPS_INITIAL_PASSWORD: pick.initialPassword,
    ATLAS_OPS_SESSION_SECRET: pick.sessionSecret,
    ATLAS_OPS_API_URL: pick.apiUrl,
    ATLAS_OPS_API_TOKEN: pick.apiToken,
  })
    .filter(([, v]) => !v)
    .map(([k]) => k);
  if ((pick.sessionSecret ?? "").length > 0 && (pick.sessionSecret ?? "").length < 32) missing.push("ATLAS_OPS_SESSION_SECRET (at least 32 characters)");
  if (missing.length) return { missing };
  return {
    missing,
    env: {
      email: pick.email!.trim().toLowerCase(),
      initialPassword: pick.initialPassword!,
      sessionSecret: pick.sessionSecret!,
      apiUrl: pick.apiUrl!.replace(/\/+$/, ""),
      apiToken: pick.apiToken!,
      telemetryKey: env.ATLAS_TELEMETRY_KEY ?? "",
      siteUrl: (env.URL ?? env.DEPLOY_PRIME_URL ?? "").replace(/\/+$/, ""),
      githubRepo: env.ATLAS_OPS_GITHUB_REPO ?? "manziosee/atlas",
    },
  };
}

// --- passwords ---------------------------------------------------------------

const SCRYPT: ScryptOptions = { N: 1 << 15, r: 8, p: 1, maxmem: 64 * 1024 * 1024 };

function scrypt(password: string, salt: Buffer, opts: ScryptOptions = SCRYPT): Promise<Buffer> {
  return new Promise((resolve, reject) => scryptCb(password.normalize("NFKC"), salt, 32, opts, (err, key) => (err ? reject(err) : resolve(key))));
}

/** "scrypt$N$r$p$salt$hash", base64url fields. */
export async function hashPassword(password: string): Promise<string> {
  const salt = randomBytes(16);
  const key = await scrypt(password, salt);
  return ["scrypt", SCRYPT.N, SCRYPT.r, SCRYPT.p, salt.toString("base64url"), key.toString("base64url")].join("$");
}

export async function verifyPassword(password: string, stored: string): Promise<boolean> {
  const [alg, n, r, p, salt, hash] = stored.split("$");
  if (alg !== "scrypt" || !salt || !hash) return false;
  const expected = Buffer.from(hash, "base64url");
  const key = await scrypt(password, Buffer.from(salt, "base64url"), { N: Number(n), r: Number(r), p: Number(p), maxmem: SCRYPT.maxmem });
  return key.length === expected.length && timingSafeEqual(key, expected);
}

function sameText(a: string, b: string) {
  const x = createHash("sha256").update(a).digest();
  const y = createHash("sha256").update(b).digest();
  return timingSafeEqual(x, y);
}

/** Why a new password is too weak, or null. */
export function passwordProblem(password: string, env: OpsEnv): string | null {
  if (password.length < 12) return "Use at least 12 characters.";
  if (password.length > 256) return "Use at most 256 characters.";
  const classes = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((re) => re.test(password)).length;
  if (password.length < 16 && classes < 3) return "Mix at least three of: lowercase, uppercase, digits and symbols (or use 16+ characters).";
  if (password === env.initialPassword) return "Choose a password different from the initial one.";
  if (password.toLowerCase().includes(env.email.split("@")[0])) return "Don't include your email name in the password.";
  return null;
}

// --- account -------------------------------------------------------------------

export interface Account {
  email: string;
  /** null until the owner sets their own password. */
  hash: string | null;
  /** Bumped on every password change; older sessions stop working. */
  version: number;
  mustChange: boolean;
  updatedAt: string;
}

const ACCOUNT_KEY = "account";

export async function loadAccount(kv: KV, env: OpsEnv): Promise<Account> {
  const saved = await kv.get<Account>(ACCOUNT_KEY);
  if (saved && saved.email === env.email) return saved;
  return { email: env.email, hash: null, version: 1, mustChange: true, updatedAt: new Date(0).toISOString() };
}

// --- sessions --------------------------------------------------------------------

export const SESSION_COOKIE = "__Host-atlas_ops";
export const SESSION_SECONDS = 12 * 60 * 60;

interface SessionClaims {
  sub: string;
  v: number;
  iat: number;
  exp: number;
}

function sign(payload: string, secret: string) {
  return createHmac("sha256", secret).update(payload).digest("base64url");
}

export function createSession(account: Account, env: OpsEnv, now = Date.now()): string {
  const claims: SessionClaims = { sub: account.email, v: account.version, iat: Math.floor(now / 1000), exp: Math.floor(now / 1000) + SESSION_SECONDS };
  const payload = Buffer.from(JSON.stringify(claims)).toString("base64url");
  return `${payload}.${sign(payload, env.sessionSecret)}`;
}

export function readSession(token: string | undefined, account: Account, env: OpsEnv, now = Date.now()): SessionClaims | null {
  if (!token) return null;
  const [payload, mac] = token.split(".");
  if (!payload || !mac) return null;
  const expected = Buffer.from(sign(payload, env.sessionSecret));
  const given = Buffer.from(mac);
  if (expected.length !== given.length || !timingSafeEqual(expected, given)) return null;
  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString()) as SessionClaims;
    if (claims.sub !== account.email || claims.v !== account.version || claims.exp * 1000 <= now) return null;
    return claims;
  } catch {
    return null;
  }
}

export function sessionCookie(token: string) {
  return `${SESSION_COOKIE}=${token}; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=${SESSION_SECONDS}`;
}

export function clearedCookie() {
  return `${SESSION_COOKIE}=; Path=/; HttpOnly; Secure; SameSite=Strict; Max-Age=0`;
}

export function cookieValue(header: string | null, name: string): string | undefined {
  for (const part of (header ?? "").split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return v.join("=");
  }
  return undefined;
}

// --- login throttling ------------------------------------------------------------

const WINDOW_MS = 15 * 60 * 1000;
const MAX_PER_IP = 5;
const MAX_PER_ACCOUNT = 20;

interface Failures {
  count: number;
  first: number;
}

function ipKey(ip: string) {
  return `fail:ip:${createHash("sha256").update(ip).digest("hex").slice(0, 32)}`;
}

async function failures(kv: KV, key: string, now: number): Promise<Failures> {
  const f = await kv.get<Failures>(key);
  return f && now - f.first < WINDOW_MS ? f : { count: 0, first: now };
}

/** Seconds until another attempt is allowed, or 0. */
export async function lockedFor(kv: KV, ip: string, now = Date.now()): Promise<number> {
  for (const [key, max] of [
    [ipKey(ip), MAX_PER_IP],
    ["fail:account", MAX_PER_ACCOUNT],
  ] as const) {
    const f = await failures(kv, key, now);
    if (f.count >= max) return Math.ceil((f.first + WINDOW_MS - now) / 1000);
  }
  return 0;
}

async function recordFailure(kv: KV, ip: string, now: number) {
  for (const key of [ipKey(ip), "fail:account"]) {
    const f = await failures(kv, key, now);
    await kv.set(key, { count: f.count + 1, first: f.first });
  }
}

// --- request handling --------------------------------------------------------------

export interface OpsRequest {
  method: string;
  url: string;
  headers: Headers;
  json(): Promise<unknown>;
  ip: string;
  country?: string;
}

export type Fetch = typeof fetch;

export interface Deps {
  kv: KV;
  env: EnvSource;
  fetch?: Fetch;
  now?: () => number;
}

function reply(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(body === null ? null : JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": "application/json",
      "Cache-Control": "no-store",
      "X-Robots-Tag": "noindex, nofollow",
      "X-Content-Type-Options": "nosniff",
      ...headers,
    },
  });
}

/** POSTs must come from this site (SameSite cookies plus an Origin check). */
function sameOrigin(req: OpsRequest, env: OpsEnv): boolean {
  if (req.headers.get("x-atlas-ops") !== "1") return false;
  const origin = req.headers.get("origin");
  if (!origin) return false;
  const self = env.siteUrl || new URL(req.url).origin;
  return origin === self || origin === new URL(req.url).origin;
}

async function body(req: OpsRequest): Promise<Record<string, unknown>> {
  try {
    const value = await req.json();
    return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

async function authenticated(req: OpsRequest, deps: Deps, env: OpsEnv) {
  const account = await loadAccount(deps.kv, env);
  const claims = readSession(cookieValue(req.headers.get("cookie"), SESSION_COOKIE), account, env, deps.now?.());
  return claims ? account : null;
}

function configured(deps: Deps): OpsEnv | Response {
  const { env, missing } = readEnv(deps.env);
  return env ?? reply(503, { error: "The operations area isn't configured.", missing });
}

/** GET /ops-api/session */
export async function handleSession(req: OpsRequest, deps: Deps): Promise<Response> {
  const env = configured(deps);
  if (env instanceof Response) return env;
  const account = await authenticated(req, deps, env);
  if (!account) return reply(200, { authenticated: false });
  return reply(200, { authenticated: true, email: account.email, mustChange: account.mustChange });
}

/** POST /ops-api/login {email, password} */
export async function handleLogin(req: OpsRequest, deps: Deps): Promise<Response> {
  const env = configured(deps);
  if (env instanceof Response) return env;
  if (req.method !== "POST" || !sameOrigin(req, env)) return reply(403, { error: "Request refused." });
  const now = deps.now?.() ?? Date.now();
  const wait = await lockedFor(deps.kv, req.ip, now);
  if (wait > 0) return reply(429, { error: `Too many attempts. Try again in ${Math.ceil(wait / 60)} minutes.` }, { "Retry-After": String(wait) });

  const input = await body(req);
  const email = String(input.email ?? "").trim().toLowerCase();
  const password = String(input.password ?? "");
  const account = await loadAccount(deps.kv, env);
  // Always run one scrypt so timing doesn't reveal which part was wrong.
  let passwordOK: boolean;
  if (account.hash) {
    passwordOK = await verifyPassword(password, account.hash);
  } else {
    await scrypt(password, randomBytes(16));
    passwordOK = sameText(password, env.initialPassword);
  }
  if (!sameText(email, account.email) || !passwordOK || password.length > 256) {
    await recordFailure(deps.kv, req.ip, now);
    return reply(401, { error: "Wrong email or password." });
  }
  await deps.kv.delete(ipKey(req.ip));
  const token = createSession(account, env, now);
  return reply(200, { authenticated: true, email: account.email, mustChange: account.mustChange }, { "Set-Cookie": sessionCookie(token) });
}

/** POST /ops-api/logout */
export async function handleLogout(req: OpsRequest, deps: Deps): Promise<Response> {
  const env = configured(deps);
  if (env instanceof Response) return env;
  if (req.method !== "POST" || !sameOrigin(req, env)) return reply(403, { error: "Request refused." });
  return reply(200, { authenticated: false }, { "Set-Cookie": clearedCookie() });
}

/** POST /ops-api/password {current, next} */
export async function handlePassword(req: OpsRequest, deps: Deps): Promise<Response> {
  const env = configured(deps);
  if (env instanceof Response) return env;
  if (req.method !== "POST" || !sameOrigin(req, env)) return reply(403, { error: "Request refused." });
  const account = await authenticated(req, deps, env);
  if (!account) return reply(401, { error: "Your session has ended. Sign in again." });
  const now = deps.now?.() ?? Date.now();
  if ((await lockedFor(deps.kv, req.ip, now)) > 0) return reply(429, { error: "Too many attempts. Try again later." });

  const input = await body(req);
  const current = String(input.current ?? "");
  const next = String(input.next ?? "");
  const currentOK = account.hash ? await verifyPassword(current, account.hash) : sameText(current, env.initialPassword);
  if (!currentOK) {
    await recordFailure(deps.kv, req.ip, now);
    return reply(400, { error: "Your current password is wrong." });
  }
  const problem = passwordProblem(next, env);
  if (problem) return reply(400, { error: problem });
  const updated: Account = { email: account.email, hash: await hashPassword(next), version: account.version + 1, mustChange: false, updatedAt: new Date(now).toISOString() };
  await deps.kv.set(ACCOUNT_KEY, updated);
  // Old sessions now fail; give this browser a fresh one.
  return reply(200, { authenticated: true, email: updated.email, mustChange: false }, { "Set-Cookie": sessionCookie(createSession(updated, env, now)) });
}

// --- dashboard data ------------------------------------------------------------------

async function getJSON<T>(fetchFn: Fetch, url: string, headers: Record<string, string>, timeoutMs = 8000): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetchFn(url, { headers: { Accept: "application/json", ...headers }, signal: controller.signal });
    if (!res.ok) {
      let detail = "";
      try {
        detail = ((await res.json()) as { error?: string }).error ?? "";
      } catch {
        // not JSON
      }
      throw new Error(`${res.status} ${res.statusText}${detail ? `: ${detail}` : ""}`);
    }
    return (await res.json()) as T;
  } finally {
    clearTimeout(timer);
  }
}

interface ServiceLite {
  id: string;
  name: string;
  node_id: string;
  address: string;
  region: string;
  version?: string;
  health: string;
  draining?: boolean;
  updated_at: string;
  tags?: string[];
}

async function allServices(fetchFn: Fetch, env: OpsEnv): Promise<ServiceLite[]> {
  const out: ServiceLite[] = [];
  let cursor = "";
  for (let i = 0; i < 20; i++) {
    const q = new URLSearchParams({ limit: "1000" });
    if (cursor) q.set("cursor", cursor);
    const page = await getJSON<{ services: ServiceLite[]; next_cursor?: string }>(fetchFn, `${env.apiUrl}/v1/services?${q}`, { Authorization: `Bearer ${env.apiToken}` });
    out.push(...(page.services ?? []));
    if (!page.next_cursor) break;
    cursor = page.next_cursor;
  }
  return out;
}

export interface GitHubStats {
  stars: number;
  forks: number;
  watchers: number;
  openIssues: number;
  releaseDownloads: number;
  releases: number;
  pushedAt: string;
}

async function githubStats(deps: Deps, env: OpsEnv, fetchFn: Fetch, now: number): Promise<GitHubStats> {
  const cached = await deps.kv.get<{ at: number; stats: GitHubStats }>("cache:github");
  if (cached && now - cached.at < 10 * 60 * 1000) return cached.stats;
  const headers = { "User-Agent": "atlas-ops-dashboard", Accept: "application/vnd.github+json" };
  const repo = await getJSON<{ stargazers_count: number; forks_count: number; subscribers_count: number; open_issues_count: number; pushed_at: string }>(
    fetchFn,
    `https://api.github.com/repos/${env.githubRepo}`,
    headers,
  );
  let downloads = 0;
  let releases = 0;
  try {
    const list = await getJSON<{ assets: { download_count: number }[] }[]>(fetchFn, `https://api.github.com/repos/${env.githubRepo}/releases?per_page=50`, headers);
    releases = list.length;
    downloads = list.reduce((n, r) => n + r.assets.reduce((m, a) => m + a.download_count, 0), 0);
  } catch {
    // releases are optional
  }
  const stats: GitHubStats = {
    stars: repo.stargazers_count,
    forks: repo.forks_count,
    watchers: repo.subscribers_count,
    openIssues: repo.open_issues_count,
    releaseDownloads: downloads,
    releases,
    pushedAt: repo.pushed_at,
  };
  await deps.kv.set("cache:github", { at: now, stats });
  return stats;
}

interface CredentialLite {
  id: string;
  name?: string;
  scopes: string[];
  source: string;
  expires_at?: string;
  expired: boolean;
  revoked?: boolean;
}

interface AuditLite {
  seq: number;
  at: string;
  actor_id: string;
  action: string;
  resource_id?: string;
}

/** Counts by state, plus the ones that expire within a week. */
function summarizeCredentials(list: CredentialLite[], now: number) {
  const week = 7 * 24 * 3600 * 1000;
  const active = list.filter((c) => !c.expired && !c.revoked);
  return {
    total: list.length,
    active: active.length,
    expired: list.filter((c) => c.expired && !c.revoked).length,
    revoked: list.filter((c) => c.revoked).length,
    managed: list.filter((c) => c.source === "managed").length,
    expiringSoon: active
      .filter((c) => c.expires_at && Date.parse(c.expires_at) - now < week)
      .map((c) => ({ id: c.id, name: c.name ?? "", scopes: c.scopes, expires_at: c.expires_at! })),
  };
}

type Settled<T> = { ok: true; value: T } | { ok: false; error: string };

async function settle<T>(p: Promise<T>): Promise<Settled<T>> {
  try {
    return { ok: true, value: await p };
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : String(err) };
  }
}

/** GET /ops-api/overview?days=N */
export async function handleOverview(req: OpsRequest, deps: Deps): Promise<Response> {
  const env = configured(deps);
  if (env instanceof Response) return env;
  const account = await authenticated(req, deps, env);
  if (!account) return reply(401, { error: "Sign in to continue." });
  if (account.mustChange) return reply(403, { error: "Change your password first.", mustChange: true });

  const days = Number(new URL(req.url).searchParams.get("days") ?? "30");
  if (!Number.isInteger(days) || days < 1 || days > 90) return reply(400, { error: "days must be between 1 and 90" });
  const fetchFn = deps.fetch ?? fetch;
  const now = deps.now?.() ?? Date.now();
  const auth = { Authorization: `Bearer ${env.apiToken}` };

  const [analytics, node, members, services, topology, github, credentials, audit] = await Promise.all([
    settle(getJSON<Record<string, unknown>>(fetchFn, `${env.apiUrl}/v1/admin/analytics?days=${days}`, auth, 15000)),
    settle(getJSON<Record<string, unknown>>(fetchFn, `${env.apiUrl}/v1/node`, auth)),
    settle(getJSON<{ members: { id: string; state: string; region: string; addr: string; since?: string }[] }>(fetchFn, `${env.apiUrl}/v1/members`, auth)),
    settle(allServices(fetchFn, env)),
    settle(getJSON<Record<string, unknown>>(fetchFn, `${env.apiUrl}/v1/topology?scope=cluster`, auth, 15000)),
    settle(githubStats(deps, env, fetchFn, now)),
    settle(getJSON<{ credentials: CredentialLite[] }>(fetchFn, `${env.apiUrl}/v1/admin/credentials`, auth)),
    settle(getJSON<{ entries: AuditLite[] }>(fetchFn, `${env.apiUrl}/v1/admin/audit?limit=20`, auth)),
  ]);

  const list = services.ok ? services.value : [];
  const health = { healthy: 0, unknown: 0, unhealthy: 0, draining: 0 };
  for (const s of list) {
    if (s.health === "healthy" || s.health === "unknown" || s.health === "unhealthy") health[s.health]++;
    if (s.draining) health.draining++;
  }
  const problems = list
    .filter((s) => s.health === "unhealthy" || s.draining)
    .map(({ id, name, node_id, address, region, health: h, draining, updated_at }) => ({ id, name, node_id, address, region, health: h, draining: !!draining, updated_at }))
    .slice(0, 100);

  return reply(200, {
    generatedAt: new Date(now).toISOString(),
    days,
    account: { email: account.email, passwordChangedAt: account.updatedAt },
    analytics: analytics.ok ? analytics.value : null,
    node: node.ok ? node.value : null,
    members: members.ok ? members.value.members : [],
    services: { total: list.length, names: new Set(list.map((s) => s.name)).size, ...health, problems },
    topology: topology.ok ? topology.value : null,
    github: github.ok ? github.value : null,
    security: {
      credentials: credentials.ok ? summarizeCredentials(credentials.value.credentials ?? [], now) : null,
      audit: audit.ok ? (audit.value.entries ?? []) : null,
    },
    errors: Object.fromEntries(
      Object.entries({ analytics, node, members, services, topology, github, credentials, audit })
        .filter(([, r]) => !r.ok)
        .map(([k, r]) => [k, (r as { error: string }).error]),
    ),
  });
}

// --- public telemetry relay -------------------------------------------------------------

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * POST /telemetry/collect: anonymous events from the site. Adds the visitor's
 * country (from the edge, never the IP itself) and forwards to Atlas with the
 * shared telemetry key. Always answers 204 so the site never waits or errs.
 */
export async function handleCollect(req: OpsRequest, deps: Deps): Promise<Response> {
  const done = new Response(null, { status: 204, headers: { "Cache-Control": "no-store" } });
  const apiUrl = (deps.env.ATLAS_OPS_API_URL ?? "").replace(/\/+$/, "");
  const key = deps.env.ATLAS_TELEMETRY_KEY ?? "";
  if (req.method !== "POST" || !apiUrl || !key) return done;
  if (Number(req.headers.get("content-length") ?? "0") > 1024) return done;
  const input = await body(req);
  const visitor = String(input.visitor_id ?? "");
  const type = String(input.type ?? "pageview");
  if (!UUID.test(visitor) || (type !== "pageview" && type !== "connect")) return done;
  const event: Record<string, string> = { type, visitor_id: visitor };
  if (type === "pageview") event.path = String(input.path ?? "/").slice(0, 256);
  else {
    event.kind = input.kind === "node" ? "node" : "demo";
    const version = String(input.version ?? "");
    if (/^[A-Za-z0-9._+-]{1,32}$/.test(version)) event.version = version;
  }
  if (req.country && /^[A-Z]{2}$/.test(req.country)) event.country = req.country;
  try {
    await (deps.fetch ?? fetch)(`${apiUrl}/v1/telemetry/event`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Atlas-Telemetry-Key": key },
      body: JSON.stringify(event),
      signal: AbortSignal.timeout(4000),
    });
  } catch {
    // Telemetry is best effort.
  }
  return done;
}
