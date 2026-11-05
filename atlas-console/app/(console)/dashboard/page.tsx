"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { useConnection } from "@/lib/connection";
import { explain, type HistoryEntry } from "@/lib/api";
import { describeEvent, healthTone, memberTone, relative } from "@/lib/format";
import { Empty, Notice, PageHeader, Skeleton, Stat, StatusBadge } from "@/components/console/ui";

export default function Overview() {
  const { members, services, feed, loading, error, lastUpdated, stream } = useCluster();
  const { client, conn } = useConnection();
  const [changes, setChanges] = useState<HistoryEntry[] | null>(null);
  const [changesError, setChangesError] = useState<string | null>(null);

  // Recent changes refresh with the feed: a new event usually means a new entry.
  const latestEvent = feed[0]?.id;
  useEffect(() => {
    if (!client) return;
    let stop = false;
    client
      .recentHistory(undefined, undefined, 8)
      .then((p) => !stop && (setChanges(p.entries), setChangesError(null)))
      .catch((err) => !stop && setChangesError(explain(err)));
    return () => {
      stop = true;
    };
  }, [client, latestEvent]);

  const stats = useMemo(() => {
    const by = (s: string) => members.filter((m) => m.state === s).length;
    const health = (h: string) => services.filter((s) => s.health === h).length;
    const regions = new Map<string, { nodes: number; alive: number; services: number; healthy: number }>();
    for (const m of members) {
      const r = regions.get(m.region || "—") ?? { nodes: 0, alive: 0, services: 0, healthy: 0 };
      r.nodes++;
      if (m.state === "alive") r.alive++;
      regions.set(m.region || "—", r);
    }
    for (const s of services) {
      const r = regions.get(s.region || "—") ?? { nodes: 0, alive: 0, services: 0, healthy: 0 };
      r.services++;
      if (s.health === "healthy") r.healthy++;
      regions.set(s.region || "—", r);
    }
    return {
      alive: by("alive"),
      attention: by("suspect") + by("dead"),
      suspect: by("suspect"),
      dead: by("dead"),
      left: by("left"),
      healthy: health("healthy"),
      unhealthy: health("unhealthy"),
      unknown: health("unknown"),
      draining: services.filter((s) => s.draining).length,
      names: new Set(services.map((s) => s.name)).size,
      regions: [...regions.entries()].sort(([a], [b]) => a.localeCompare(b)),
    };
  }, [members, services]);

  const attention = [
    ...members.filter((m) => m.state === "suspect" || m.state === "dead").map((m) => ({ key: `m-${m.id}`, href: "/nodes/", text: `Node ${m.id} is ${m.state}`, tone: memberTone[m.state] })),
    ...services.filter((s) => s.health === "unhealthy").map((s) => ({ key: `s-${s.id}`, href: `/services/?id=${encodeURIComponent(s.id)}`, text: `${s.name} (${s.id}) is unhealthy`, tone: healthTone.unhealthy })),
  ].slice(0, 6);

  return (
    <>
      <PageHeader
        title="Cluster overview"
        desc={
          <>
            As seen by <span className="font-mono text-fg">{conn?.info.node.id}</span>. Each node keeps its own replica, so views converge within seconds.
          </>
        }
      />
      {error ? (
        <div className="mb-6">
          <Notice tone="error" title="Couldn't load the cluster">
            {explain(error)} {lastUpdated && <>Showing data from {relative(lastUpdated)}.</>}
          </Notice>
        </div>
      ) : stream === "offline" ? (
        <div className="mb-6">
          <Notice tone="warn">The live event stream is unavailable, so the console is polling every 15 seconds.</Notice>
        </div>
      ) : null}

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {loading && !lastUpdated ? (
          Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-[118px]" />)
        ) : (
          <>
            <Stat label="Nodes" value={members.length} hint={`${stats.left} left the cluster`} />
            <Stat label="Alive" value={stats.alive} tone="ok" hint={`of ${members.length - stats.left} active members`} />
            <Stat label="Need attention" value={stats.attention} tone={stats.attention ? "warn" : undefined} hint={`${stats.suspect} suspect · ${stats.dead} dead`} />
            <Stat label="Service instances" value={services.length} hint={`${stats.names} services · ${stats.draining} draining`} />
          </>
        )}
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-3">
        <section className="panel p-5 xl:col-span-2" aria-labelledby="health-title">
          <h2 id="health-title" className="font-medium">
            Service health
          </h2>
          <div className="mt-4 flex h-3 overflow-hidden rounded-full bg-white/[0.05]" role="img" aria-label={`${stats.healthy} healthy, ${stats.unknown} unknown, ${stats.unhealthy} unhealthy`}>
            {services.length > 0 && (
              <>
                <div className="bg-ok" style={{ width: `${(stats.healthy / services.length) * 100}%` }} />
                <div className="bg-warn" style={{ width: `${(stats.unknown / services.length) * 100}%` }} />
                <div className="bg-bad" style={{ width: `${(stats.unhealthy / services.length) * 100}%` }} />
              </>
            )}
          </div>
          <div className="mt-3 flex flex-wrap gap-2">
            <StatusBadge tone="ok">{stats.healthy} healthy</StatusBadge>
            <StatusBadge tone="warn">{stats.unknown} unknown</StatusBadge>
            <StatusBadge tone="bad">{stats.unhealthy} unhealthy</StatusBadge>
            <StatusBadge tone="info">{stats.draining} draining</StatusBadge>
          </div>

          <h3 className="mt-7 text-sm font-medium text-muted">By region</h3>
          <div className="mt-2 overflow-x-auto">
            <table className="w-full min-w-[420px] text-sm">
              <thead className="text-left text-xs text-faint">
                <tr>
                  <th className="py-2 font-normal">Region</th>
                  <th className="py-2 font-normal">Nodes alive</th>
                  <th className="py-2 font-normal">Instances</th>
                  <th className="py-2 font-normal">Healthy</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {stats.regions.map(([region, r]) => (
                  <tr key={region}>
                    <td className="py-2 font-mono">{region}</td>
                    <td className="py-2 tabular-nums">
                      {r.alive}/{r.nodes}
                    </td>
                    <td className="py-2 tabular-nums">{r.services}</td>
                    <td className="py-2 tabular-nums">{r.healthy}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section className="panel p-5" aria-labelledby="attention-title">
          <h2 id="attention-title" className="font-medium">
            Needs attention
          </h2>
          {attention.length === 0 ? (
            <p className="mt-4 flex items-center gap-2 text-sm text-muted">
              <StatusBadge tone="ok">All clear</StatusBadge> No suspect or dead nodes, no unhealthy instances.
            </p>
          ) : (
            <ul className="mt-3 space-y-2">
              {attention.map((a) => (
                <li key={a.key}>
                  <Link href={a.href} className="flex items-center justify-between gap-3 rounded-lg px-2 py-1.5 text-sm hover:bg-white/[0.04]">
                    <span className="min-w-0 truncate">{a.text}</span>
                    <StatusBadge tone={a.tone}>{a.tone === "bad" ? "act" : "check"}</StatusBadge>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <section className="panel p-5" aria-labelledby="live-title">
          <div className="flex items-center justify-between">
            <h2 id="live-title" className="font-medium">
              Live activity
            </h2>
            <Link href="/activity/" className="flex items-center gap-1 text-xs text-muted hover:text-fg">
              All activity <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </div>
          {feed.length === 0 ? (
            <Empty title="Waiting for events" desc="Membership and service changes appear here the moment they happen." />
          ) : (
            <ul className="mt-3 space-y-2">
              {feed.slice(0, 8).map((e) => {
                const d = describeEvent(e);
                return (
                  <li key={`${e.id}-${e.receivedAt}`} className="flex items-start justify-between gap-3 text-sm">
                    <span className="min-w-0">{d.text}</span>
                    <span className="shrink-0 font-mono text-xs text-faint">{relative(e.at)}</span>
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        <section className="panel p-5" aria-labelledby="changes-title">
          <div className="flex items-center justify-between">
            <h2 id="changes-title" className="font-medium">
              Recent changes
            </h2>
            <Link href="/activity/?tab=history" className="flex items-center gap-1 text-xs text-muted hover:text-fg">
              History <ArrowRight className="h-3.5 w-3.5" />
            </Link>
          </div>
          {changesError ? (
            <p className="mt-3 text-sm text-muted">{changesError}</p>
          ) : changes === null ? (
            <div className="mt-3 space-y-2">
              <Skeleton className="h-5" />
              <Skeleton className="h-5" />
              <Skeleton className="h-5" />
            </div>
          ) : changes.length === 0 ? (
            <Empty title="No changes recorded yet" desc="Registrations, updates and health changes are kept for 7 days by default." />
          ) : (
            <ul className="mt-3 space-y-2">
              {changes.map((c) => (
                <li key={`${c.service_id}-${c.seq}`} className="flex items-start justify-between gap-3 text-sm">
                  <Link href={`/services/?id=${encodeURIComponent(c.service_id)}`} className="min-w-0 hover:underline">
                    <span className="font-mono">{c.name}</span> <span className="text-muted">{c.change}</span>
                    {c.change === "health" && c.service ? <span className="text-muted"> → {c.service.health}</span> : null}
                  </Link>
                  <span className="shrink-0 font-mono text-xs text-faint">{relative(c.at)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </>
  );
}
