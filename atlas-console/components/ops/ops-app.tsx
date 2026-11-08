"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Activity, Boxes, Globe2, KeyRound, Loader2, LogOut, RefreshCw, ShieldCheck, Star, Users } from "lucide-react";
import { Logo } from "@/components/logo";
import { Field, Modal, Notice, Skeleton, StatusBadge } from "@/components/console/ui";
import { cn, relative, timestamp } from "@/lib/format";

// --- API -----------------------------------------------------------------------

interface Session {
  authenticated: boolean;
  email?: string;
  mustChange?: boolean;
}

interface Day {
  date: string;
  page_views: number;
  unique_visitors: number;
  unique_visitors_capped: boolean;
  systems: number;
  node_connections: number;
  demo_connections: number;
}

interface Analytics {
  days: number;
  nodes_included: number;
  node_errors: { node_id: string; error: string }[];
  daily: Day[];
  pages: { page: string; page_views: number }[];
  countries: { country: string; page_views: number; visitors: number }[];
  versions: { version: string; connections: number }[];
  api_requests_since_start: number;
  api_errors_since_start: number;
  api_requests_by_route: { route: string; count: number; errors: number }[];
}

interface Overview {
  generatedAt: string;
  days: number;
  account: { email: string; passwordChangedAt: string };
  analytics: Analytics | null;
  node: { node: { id: string; region: string }; version: string } | null;
  members: { id: string; state: string; region: string; addr: string; since?: string }[];
  services: {
    total: number;
    names: number;
    healthy: number;
    unknown: number;
    unhealthy: number;
    draining: number;
    problems: { id: string; name: string; node_id: string; address: string; region: string; health: string; draining: boolean; updated_at: string }[];
  };
  topology: { nodes: { id: string; view?: { peers: { id: string; rtt_ms?: number }[] }; error?: string }[] } | null;
  github: { stars: number; forks: number; watchers: number; openIssues: number; releaseDownloads: number; releases: number; pushedAt: string } | null;
  errors: Record<string, string>;
}

async function call<T>(path: string, init?: { method?: string; body?: unknown; signal?: AbortSignal }): Promise<{ status: number; data: T }> {
  const res = await fetch(path, {
    method: init?.method ?? "GET",
    credentials: "same-origin",
    cache: "no-store",
    signal: init?.signal,
    headers: { Accept: "application/json", ...(init?.body !== undefined ? { "Content-Type": "application/json", "X-Atlas-Ops": "1" } : init?.method === "POST" ? { "X-Atlas-Ops": "1" } : {}) },
    body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  let data: unknown = {};
  try {
    data = await res.json();
  } catch {
    // empty body
  }
  return { status: res.status, data: data as T };
}

// --- helpers -------------------------------------------------------------------

const fmt = new Intl.NumberFormat("en");
const n = (v: number | undefined) => (v === undefined ? "—" : fmt.format(v));

function flag(code: string) {
  if (!/^[A-Z]{2}$/.test(code)) return "🏳️";
  return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

function countryName(code: string) {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function Card({ title, icon: Icon, children, className, aside }: { title: string; icon?: typeof Users; children: ReactNode; className?: string; aside?: ReactNode }) {
  return (
    <section className={cn("panel p-5", className)} aria-label={title}>
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="flex items-center gap-2 font-medium">
          {Icon && <Icon className="h-4 w-4 text-lime" />} {title}
        </h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

function Kpi({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: ReactNode; tone?: "ok" | "warn" | "bad" }) {
  return (
    <div className="panel p-4">
      <p className="text-[0.7rem] uppercase tracking-wider text-faint">{label}</p>
      <p className={cn("mt-1.5 font-mono text-2xl font-semibold tabular-nums", tone === "ok" && "text-ok", tone === "warn" && "text-warn", tone === "bad" && "text-bad")}>{value}</p>
      {hint && <p className="mt-1 text-xs text-muted">{hint}</p>}
    </div>
  );
}

function Bar({ value, max, color = "bg-teal" }: { value: number; max: number; color?: string }) {
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/[0.06]">
      <div className={cn("h-full rounded-full", color)} style={{ width: `${max ? Math.max(2, (value / max) * 100) : 0}%` }} />
    </div>
  );
}

/** Daily page views (bars) and unique visitors (line). */
function TrafficChart({ days }: { days: Day[] }) {
  const W = 760;
  const H = 220;
  const pad = { l: 36, r: 10, t: 10, b: 24 };
  const max = Math.max(1, ...days.map((d) => Math.max(d.page_views, d.unique_visitors)));
  const step = (W - pad.l - pad.r) / Math.max(days.length, 1);
  const y = (v: number) => pad.t + (H - pad.t - pad.b) * (1 - v / max);
  const line = days.map((d, i) => `${i ? "L" : "M"}${pad.l + step * i + step / 2},${y(d.unique_visitors)}`).join(" ");
  const ticks = [0, 0.5, 1].map((f) => Math.round(max * f));
  const labelEvery = Math.ceil(days.length / 8);
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label="Daily page views and unique visitors">
      {ticks.map((t) => (
        <g key={t}>
          <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} stroke="rgb(255 255 255 / 0.06)" />
          <text x={pad.l - 6} y={y(t) + 4} textAnchor="end" className="fill-current text-[10px] text-faint">
            {fmt.format(t)}
          </text>
        </g>
      ))}
      {days.map((d, i) => (
        <g key={d.date}>
          <rect x={pad.l + step * i + step * 0.15} width={step * 0.7} y={y(d.page_views)} height={H - pad.b - y(d.page_views)} rx={2} fill="rgb(47 211 181 / 0.35)">
            <title>{`${d.date}: ${d.page_views} page views, ${d.unique_visitors} visitors, ${d.systems} systems`}</title>
          </rect>
          {i % labelEvery === 0 && (
            <text x={pad.l + step * i + step / 2} y={H - 6} textAnchor="middle" className="fill-current text-[10px] text-faint">
              {d.date.slice(5)}
            </text>
          )}
        </g>
      ))}
      <path d={line} fill="none" stroke="#b6f27a" strokeWidth={2} />
    </svg>
  );
}

// --- screens -----------------------------------------------------------------------

function Centered({ children }: { children: ReactNode }) {
  return (
    <div className="relative grid min-h-dvh place-items-center px-4 py-10">
      <div className="pointer-events-none absolute inset-0 -z-10 bg-[radial-gradient(50%_40%_at_50%_0%,rgb(47_211_181/0.16),transparent_70%)]" />
      <div className="w-full max-w-sm">{children}</div>
    </div>
  );
}

function LoginForm({ onDone }: { onDone: (s: Session) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const { status, data } = await call<Session & { error?: string }>("/ops-api/login", { method: "POST", body: { email, password } });
      if (status === 200) onDone(data);
      else setError(data.error ?? "Sign-in failed.");
    } catch {
      setError("Couldn't reach the server. Check your connection.");
    } finally {
      setBusy(false);
      setPassword("");
    }
  };
  return (
    <Centered>
      <form onSubmit={submit} className="panel p-7">
        <Logo className="h-10 w-10" />
        <h1 className="mt-5 text-xl font-semibold">Atlas operations</h1>
        <p className="mt-1 text-sm text-muted">Private. Sign in to continue.</p>
        <div className="mt-6 space-y-4">
          <Field label="Email">
            <input className="field" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </Field>
          <Field label="Password">
            <input className="field" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </Field>
          {error && <Notice tone="error">{error}</Notice>}
          <button className="btn btn-lime w-full" disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />} Sign in
          </button>
        </div>
      </form>
    </Centered>
  );
}

function PasswordForm({ forced, onDone, onCancel }: { forced: boolean; onDone: (s: Session) => void; onCancel?: () => void }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (next !== repeat) return setError("The new passwords don't match.");
    setBusy(true);
    try {
      const { status, data } = await call<Session & { error?: string }>("/ops-api/password", { method: "POST", body: { current, next } });
      if (status === 200) {
        setOk(true);
        onDone(data);
      } else setError(data.error ?? "Couldn't change the password.");
    } catch {
      setError("Couldn't reach the server.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="space-y-4">
      {forced && <Notice tone="info">Choose your own password before opening the dashboard. The initial password stops working afterwards.</Notice>}
      <Field label="Current password">
        <input className="field" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
      </Field>
      <Field label="New password" hint="12+ characters mixing upper and lower case, digits and symbols, or 16+ characters.">
        <input className="field" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required minLength={12} />
      </Field>
      <Field label="Repeat new password">
        <input className="field" type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required />
      </Field>
      {error && <Notice tone="error">{error}</Notice>}
      {ok && <Notice tone="ok">Password changed. Other sessions were signed out.</Notice>}
      <div className="flex justify-end gap-2">
        {onCancel && (
          <button type="button" className="btn btn-outline" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button className="btn btn-lime" disabled={busy}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />} Change password
        </button>
      </div>
    </form>
  );
}

function Dashboard({ session, onSession }: { session: Session; onSession: (s: Session) => void }) {
  const [days, setDays] = useState(30);
  const [data, setData] = useState<Overview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [account, setAccount] = useState(false);
  const inflight = useRef<AbortController | null>(null);

  const load = useCallback(async (range: number) => {
    inflight.current?.abort(); // a newer range wins
    const controller = new AbortController();
    inflight.current = controller;
    setLoading(true);
    try {
      const { status, data: body } = await call<Overview & { error?: string; mustChange?: boolean }>(`/ops-api/overview?days=${range}`, { signal: controller.signal });
      if (controller.signal.aborted) return;
      if (status === 401) return onSession({ authenticated: false });
      if (status === 403 && body.mustChange) return onSession({ ...session, mustChange: true });
      if (status !== 200) throw new Error(body.error ?? `Server answered ${status}`);
      setData(body);
      setError(null);
    } catch (err) {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (inflight.current === controller) setLoading(false);
    }
  }, [onSession, session]);

  useEffect(() => {
    void load(days);
    const t = setInterval(() => void load(days), 60_000);
    return () => {
      clearInterval(t);
      inflight.current?.abort();
    };
  }, [days, load]);

  const a = data?.analytics;
  const totals = useMemo(() => {
    const d = a?.daily ?? [];
    const sum = (k: keyof Day) => d.reduce((s, x) => s + Number(x[k] || 0), 0);
    const today = d[d.length - 1];
    return {
      views: sum("page_views"),
      visitorDays: sum("unique_visitors"),
      systemDays: sum("systems"),
      nodeConnections: sum("node_connections"),
      demoConnections: sum("demo_connections"),
      todayVisitors: today?.unique_visitors ?? 0,
      todaySystems: today?.systems ?? 0,
      capped: d.some((x) => x.unique_visitors_capped),
    };
  }, [a]);
  const alive = data?.members.filter((m) => m.state === "alive").length ?? 0;
  const latency = useMemo(() => {
    const all: { a: string; b: string; ms: number }[] = [];
    for (const node of data?.topology?.nodes ?? []) for (const p of node.view?.peers ?? []) if (p.rtt_ms !== undefined) all.push({ a: node.id, b: p.id, ms: p.rtt_ms });
    all.sort((x, y) => x.ms - y.ms);
    return { median: all[Math.floor(all.length / 2)]?.ms, worst: all[all.length - 1] };
  }, [data]);
  const maxCountry = Math.max(1, ...(a?.countries.map((c) => c.page_views) ?? [0]));
  const maxPage = Math.max(1, ...(a?.pages.map((p) => p.page_views) ?? [0]));
  const maxVersion = Math.max(1, ...(a?.versions.map((v) => v.connections) ?? [0]));

  const signOut = async () => {
    await call("/ops-api/logout", { method: "POST" }).catch(() => undefined);
    onSession({ authenticated: false });
  };

  return (
    <div className="min-h-dvh">
      <header className="glass sticky top-0 z-40 border-b border-line">
        <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-4">
          <Logo className="h-7 w-7" />
          <span className="font-semibold">Atlas operations</span>
          <span className="rounded-full border border-warn/40 bg-warn/10 px-2 py-0.5 text-[0.68rem] text-warn">private</span>
          <div className="ml-auto flex items-center gap-2 text-xs">
            <label className="flex items-center gap-1.5 text-muted">
              <span className="sr-only sm:not-sr-only">Range</span>
              <select className="field w-auto !py-1.5 text-xs" value={days} onChange={(e) => setDays(Number(e.target.value))}>
                <option value={1}>Today</option>
                <option value={7}>7 days</option>
                <option value={30}>30 days</option>
                <option value={90}>90 days</option>
              </select>
            </label>
            <button className="rounded-lg p-2 text-muted hover:bg-white/5 hover:text-fg" onClick={() => load(days)} aria-label="Refresh">
              <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
            </button>
            <button className="hidden rounded-lg px-2 py-2 text-muted hover:bg-white/5 hover:text-fg sm:block" onClick={() => setAccount(true)}>
              {session.email}
            </button>
            <button className="flex items-center gap-1.5 rounded-lg px-2 py-2 text-muted hover:bg-white/5 hover:text-fg" onClick={signOut}>
              <LogOut className="h-4 w-4" /> <span className="hidden sm:inline">Sign out</span>
            </button>
          </div>
        </div>
      </header>

      <main id="main" className="mx-auto max-w-7xl space-y-6 p-4 sm:p-6">
        {error && <Notice tone="error" title="Couldn't load the dashboard">{error}</Notice>}
        {data && Object.keys(data.errors).length > 0 && (
          <Notice tone="warn" title="Some sources didn't answer">
            {Object.entries(data.errors).map(([k, v]) => (
              <span key={k} className="block">
                <span className="font-mono">{k}</span>: {v}
              </span>
            ))}
          </Notice>
        )}
        {a && a.node_errors.length > 0 && (
          <Notice tone="warn" title={`Analytics from ${a.nodes_included} node(s); some nodes didn't report`}>
            {a.node_errors.map((e) => `${e.node_id}: ${e.error}`).join(" · ")}
          </Notice>
        )}

        {!data ? (
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            {Array.from({ length: 8 }, (_, i) => (
              <Skeleton key={i} className="h-24" />
            ))}
          </div>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
              <Kpi label="Visitors today" value={n(totals.todayVisitors)} hint={`${n(totals.visitorDays)} browser-days in range${totals.capped ? " · capped" : ""}`} />
              <Kpi label="Page views" value={n(totals.views)} hint={`last ${data.days} day${data.days === 1 ? "" : "s"}`} />
              <Kpi label="Systems using Atlas" value={n(totals.todaySystems)} hint={`today · ${n(totals.systemDays)} system-days, ${n(totals.nodeConnections)} connections in range`} />
              <Kpi label="Countries" value={n(a?.countries.length)} hint={a?.countries[0] ? `top: ${countryName(a.countries[0].country)}` : "no data yet"} />
              <Kpi label="Demo opens" value={n(totals.demoConnections)} hint="read-only demo connections" />
              <Kpi label="API requests" value={n(a?.api_requests_since_start)} hint={`${n(a?.api_errors_since_start)} server errors since restart`} tone={a && a.api_errors_since_start > 0 ? "warn" : undefined} />
              <Kpi label="Nodes alive" value={`${alive}/${data.members.length}`} tone={alive === data.members.length ? "ok" : "warn"} hint={data.node ? `${data.node.version} · ${data.node.node.region}` : undefined} />
              <Kpi label="Unhealthy services" value={n(data.services.unhealthy)} tone={data.services.unhealthy ? "bad" : "ok"} hint={`${n(data.services.total)} instances · ${n(data.services.names)} services`} />
            </div>

            <div className="grid gap-6 xl:grid-cols-3">
              <Card title="Traffic" icon={Activity} className="xl:col-span-2" aside={<span className="text-xs text-muted">bars: page views · line: unique visitors</span>}>
                {a ? <TrafficChart days={a.daily} /> : <p className="text-sm text-muted">Analytics unavailable.</p>}
              </Card>
              <Card title="Countries" icon={Globe2} aside={<span className="text-xs text-muted">views · browser-days</span>}>
                {!a?.countries.length ? (
                  <p className="text-sm text-muted">No country data yet. Countries are added by the site&apos;s edge function.</p>
                ) : (
                  <ul className="max-h-[260px] space-y-2.5 overflow-y-auto pr-1">
                    {a.countries.map((c) => (
                      <li key={c.country}>
                        <div className="flex items-center justify-between gap-2 text-sm">
                          <span className="min-w-0 truncate">
                            <span aria-hidden>{flag(c.country)}</span> {countryName(c.country)}
                          </span>
                          <span className="shrink-0 font-mono text-xs text-muted">
                            {n(c.page_views)} · {n(c.visitors)}
                          </span>
                        </div>
                        <Bar value={c.page_views} max={maxCountry} />
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
            </div>

            <div className="grid gap-6 lg:grid-cols-3">
              <Card title="Atlas in use" icon={Boxes}>
                <p className="mb-3 text-xs text-muted">Console connections to people&apos;s own nodes, by node version.</p>
                {!a?.versions.length ? (
                  <p className="text-sm text-muted">No one has connected their own node yet.</p>
                ) : (
                  <ul className="space-y-2.5">
                    {a.versions.slice(0, 10).map((v) => (
                      <li key={v.version}>
                        <div className="flex justify-between text-sm">
                          <span className="font-mono">{v.version}</span>
                          <span className="font-mono text-xs text-muted">{n(v.connections)}</span>
                        </div>
                        <Bar value={v.connections} max={maxVersion} color="bg-lime" />
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
              <Card title="Top pages" icon={Users}>
                {!a?.pages.length ? (
                  <p className="text-sm text-muted">No page views yet.</p>
                ) : (
                  <ul className="space-y-2.5">
                    {a.pages.slice(0, 10).map((p) => (
                      <li key={p.page}>
                        <div className="flex justify-between text-sm">
                          <span className="font-mono">{p.page}</span>
                          <span className="font-mono text-xs text-muted">{n(p.page_views)}</span>
                        </div>
                        <Bar value={p.page_views} max={maxPage} />
                      </li>
                    ))}
                  </ul>
                )}
              </Card>
              <Card title="Open source" icon={Star}>
                {!data.github ? (
                  <p className="text-sm text-muted">GitHub stats unavailable.</p>
                ) : (
                  <dl className="grid grid-cols-2 gap-3 text-sm">
                    {(
                      [
                        ["Stars", data.github.stars],
                        ["Forks", data.github.forks],
                        ["Watchers", data.github.watchers],
                        ["Open issues & PRs", data.github.openIssues],
                        ["Releases", data.github.releases],
                        ["Release downloads", data.github.releaseDownloads],
                      ] as const
                    ).map(([k, v]) => (
                      <div key={k} className="rounded-xl border border-line p-3">
                        <dt className="text-xs text-muted">{k}</dt>
                        <dd className="mt-1 font-mono text-lg">{n(v)}</dd>
                      </div>
                    ))}
                    <p className="col-span-2 text-xs text-faint">Last push {relative(data.github.pushedAt)}</p>
                  </dl>
                )}
              </Card>
            </div>

            <div className="grid gap-6 xl:grid-cols-2">
              <Card title="Services needing attention" icon={ShieldCheck} aside={<StatusBadge tone={data.services.unhealthy ? "bad" : "ok"}>{data.services.unhealthy ? `${data.services.unhealthy} unhealthy` : "all healthy"}</StatusBadge>}>
                <div className="mb-3 flex flex-wrap gap-2">
                  <StatusBadge tone="ok">{n(data.services.healthy)} healthy</StatusBadge>
                  <StatusBadge tone="warn">{n(data.services.unknown)} unknown</StatusBadge>
                  <StatusBadge tone="bad">{n(data.services.unhealthy)} unhealthy</StatusBadge>
                  <StatusBadge tone="info">{n(data.services.draining)} draining</StatusBadge>
                </div>
                {data.services.problems.length === 0 ? (
                  <p className="text-sm text-muted">Every instance is healthy and in rotation.</p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className="w-full min-w-[520px] text-sm">
                      <thead className="text-left text-xs text-faint">
                        <tr>
                          <th className="py-2 font-normal">Service</th>
                          <th className="py-2 font-normal">State</th>
                          <th className="py-2 font-normal">Address</th>
                          <th className="py-2 font-normal">Owner</th>
                          <th className="py-2 font-normal">Changed</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-line">
                        {data.services.problems.map((s) => (
                          <tr key={s.id}>
                            <td className="py-2">
                              <span className="font-medium">{s.name}</span> <span className="font-mono text-xs text-faint">{s.id}</span>
                            </td>
                            <td className="py-2">{s.health === "unhealthy" ? <StatusBadge tone="bad">unhealthy</StatusBadge> : <StatusBadge tone="info">draining</StatusBadge>}</td>
                            <td className="py-2 font-mono text-xs text-muted">{s.address}</td>
                            <td className="py-2 font-mono text-xs text-muted">{s.node_id}</td>
                            <td className="py-2 text-xs text-muted" title={timestamp(s.updated_at)}>{relative(s.updated_at)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </Card>

              <Card title="Cluster" icon={Activity} aside={<span className="text-xs text-muted">median RTT {latency.median !== undefined ? `${latency.median.toFixed(2)} ms` : "—"}</span>}>
                <ul className="divide-y divide-line text-sm">
                  {data.members.map((m) => (
                    <li key={m.id} className="flex items-center justify-between gap-3 py-2">
                      <span className="min-w-0 truncate font-mono">{m.id}</span>
                      <span className="flex items-center gap-2">
                        <span className="text-xs text-muted">{m.region}</span>
                        <StatusBadge tone={m.state === "alive" ? "ok" : m.state === "suspect" ? "warn" : m.state === "dead" ? "bad" : "muted"}>{m.state}</StatusBadge>
                      </span>
                    </li>
                  ))}
                </ul>
                {latency.worst && (
                  <p className="mt-3 text-xs text-muted">
                    Slowest link: <span className="font-mono">{latency.worst.a}</span> → <span className="font-mono">{latency.worst.b}</span> {latency.worst.ms.toFixed(1)} ms
                  </p>
                )}
                {a && a.api_requests_by_route.some((r) => r.errors > 0) && (
                  <div className="mt-4">
                    <p className="text-xs uppercase tracking-wider text-faint">API routes with server errors</p>
                    <ul className="mt-2 space-y-1 text-sm">
                      {a.api_requests_by_route
                        .filter((r) => r.errors > 0)
                        .slice(0, 8)
                        .map((r) => (
                          <li key={r.route} className="flex justify-between gap-3">
                            <span className="truncate font-mono text-xs">{r.route}</span>
                            <span className="font-mono text-xs text-bad">
                              {n(r.errors)} / {n(r.count)}
                            </span>
                          </li>
                        ))}
                    </ul>
                  </div>
                )}
              </Card>
            </div>

            <p className="text-xs text-faint">
              Updated {relative(data.generatedAt)} · analytics from {a?.nodes_included ?? 0} node(s) · visitors are counted per browser per day without storing the
              browser ID · API counters reset when a node restarts.
            </p>
          </>
        )}
      </main>

      <Modal open={account} onOpenChange={setAccount} title="Account" description={<>Signed in as {session.email}. Password last changed {data ? relative(data.account.passwordChangedAt) : "—"}.</>}>
        <PasswordForm forced={false} onDone={onSession} onCancel={() => setAccount(false)} />
      </Modal>
    </div>
  );
}

/** The private operations app: sign-in, forced password change, dashboard. */
export function OpsApp() {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    call<Session & { error?: string }>("/ops-api/session")
      .then(({ status, data }) => (status === 200 ? setSession(data) : setError(data.error ?? "The operations area is unavailable.")))
      .catch(() => setError("Couldn't reach the server."));
  }, []);

  return (
    <div data-atlas-private="">
      {error ? (
        <Centered>
          <Notice tone="error">{error}</Notice>
        </Centered>
      ) : !session ? (
        <Centered>
          <Loader2 className="mx-auto h-6 w-6 animate-spin text-muted" />
        </Centered>
      ) : !session.authenticated ? (
        <LoginForm onDone={setSession} />
      ) : session.mustChange ? (
        <Centered>
          <div className="panel p-7">
            <h1 className="text-xl font-semibold">Set your password</h1>
            <p className="mb-5 mt-1 text-sm text-muted">Signed in as {session.email}.</p>
            <PasswordForm forced onDone={setSession} />
          </div>
        </Centered>
      ) : (
        <Dashboard session={session} onSession={setSession} />
      )}
    </div>
  );
}
