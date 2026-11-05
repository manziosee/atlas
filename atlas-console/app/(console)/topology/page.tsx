"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { useConnection } from "@/lib/connection";
import { AtlasError, explain, type ClusterTopology } from "@/lib/api";
import { band, bandColor, buildTopology, formatMs } from "@/lib/topology";
import { cn, memberTone, relative } from "@/lib/format";
import { Notice, PageHeader, Skeleton, Stat, StatusBadge } from "@/components/console/ui";
import { ClusterGraph } from "@/components/console/cluster-graph";
import { WorldMap } from "@/components/console/world-map";

type View = "graph" | "map" | "matrix";

export default function TopologyPage() {
  const { client } = useConnection();
  const { members, services } = useCluster();
  const [topology, setTopology] = useState<ClusterTopology | null>(null);
  const [error, setError] = useState<{ text: string; unsupported: boolean } | null>(null);
  const [loading, setLoading] = useState(true);
  const [view, setView] = useState<View>("graph");
  const [selected, setSelected] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!client) return;
    try {
      setTopology(await client.clusterTopology());
      setError(null);
    } catch (err) {
      setError({ text: explain(err), unsupported: err instanceof AtlasError && err.code === "route_not_found" });
    } finally {
      setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    void load();
    const t = setInterval(load, 10_000);
    return () => clearInterval(t);
  }, [load]);

  const servicesPerNode = useMemo(() => {
    const m = new Map<string, number>();
    for (const s of services) m.set(s.node_id, (m.get(s.node_id) ?? 0) + 1);
    return m;
  }, [services]);
  const model = useMemo(() => buildTopology(topology, members, servicesPerNode), [topology, members, servicesPerNode]);
  const measured = model.edges.map((e) => e.rtt);
  const median = measured.length ? [...measured].sort((a, b) => a - b)[Math.floor(measured.length / 2)] : undefined;
  const worst = model.edges.reduce<(typeof model.edges)[number] | undefined>((w, e) => (!w || e.rtt > w.rtt ? e : w), undefined);
  const regions = new Set(model.nodes.map((n) => n.region).filter(Boolean)).size;
  const node = model.nodes.find((n) => n.id === selected) ?? null;

  return (
    <>
      <PageHeader title="Cluster map" desc="Every node probes its peers continuously. These are the smoothed round-trip times each node measured, gathered by the node you're connected to.">
        <button className="btn btn-outline !py-1.5 text-xs" onClick={() => load()}>
          <RefreshCw className="h-3.5 w-3.5" /> Refresh
        </button>
      </PageHeader>

      {error && (
        <div className="mb-5">
          <Notice tone={error.unsupported ? "info" : "error"} title={error.unsupported ? "Latency isn't available from this node" : "Couldn't load latency"}>
            {error.unsupported ? "This node runs an Atlas version without the topology API. The map shows membership only; upgrade the node to see latency." : error.text}
          </Notice>
        </div>
      )}
      {topology?.truncated && (
        <div className="mb-5">
          <Notice tone="info">This cluster is larger than one request asks, so some nodes show membership only.</Notice>
        </div>
      )}

      <div className="mb-6 grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="Nodes" value={model.nodes.length} hint={`${regions} region${regions === 1 ? "" : "s"}`} />
        <Stat label="Measured links" value={model.edges.length} hint="peer pairs with a probe RTT" />
        <Stat label="Median RTT" value={formatMs(median)} />
        <Stat label="Slowest link" value={formatMs(worst?.rtt)} hint={worst ? `${worst.a} ↔ ${worst.b}` : undefined} tone={worst && band(worst.rtt) === "bad" ? "warn" : undefined} />
      </div>

      <div className="grid gap-6 xl:grid-cols-[1fr_320px]">
        <section className="panel p-4 sm:p-5">
          <div className="mb-4 flex flex-wrap items-center gap-3">
            <div role="tablist" aria-label="Map view" className="flex gap-1 rounded-full border border-line p-1">
              {(
                [
                  ["graph", "Graph"],
                  ["map", "World map"],
                  ["matrix", "Latency matrix"],
                ] as const
              ).map(([id, label]) => (
                <button key={id} role="tab" aria-selected={view === id} onClick={() => setView(id)} className={cn("rounded-full px-3 py-1 text-xs", view === id ? "bg-lime/15 text-lime" : "text-muted hover:text-fg")}>
                  {label}
                </button>
              ))}
            </div>
            <div className="ml-auto flex flex-wrap items-center gap-3 text-xs text-muted" aria-label="Legend">
              {(["fast", "ok", "slow", "bad"] as const).map((b) => (
                <span key={b} className="flex items-center gap-1.5">
                  <span className="h-0.5 w-4 rounded" style={{ background: bandColor[b] }} />
                  {b === "fast" ? "< 5 ms" : b === "ok" ? "< 50 ms" : b === "slow" ? "< 150 ms" : "≥ 150 ms"}
                </span>
              ))}
            </div>
          </div>

          {loading && !topology && !error ? (
            <Skeleton className="h-[420px]" />
          ) : model.nodes.length === 0 ? (
            <p className="py-16 text-center text-sm text-muted">No members yet.</p>
          ) : view === "graph" ? (
            <ClusterGraph nodes={model.nodes} edges={model.edges} selected={selected} onSelect={setSelected} />
          ) : view === "map" ? (
            <WorldMap nodes={model.nodes} edges={model.edges} selected={selected} onSelect={setSelected} />
          ) : (
            <div className="overflow-auto">
              <table className="text-xs">
                <caption className="mb-2 text-left text-muted">Row: the measuring node. Column: the peer it probed.</caption>
                <thead>
                  <tr>
                    <th className="sticky left-0 bg-panel p-2" />
                    {model.nodes.map((n) => (
                      <th key={n.id} scope="col" className="max-w-[90px] truncate p-2 text-left font-mono font-normal text-muted" title={n.id}>
                        {n.id}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {model.nodes.map((row) => (
                    <tr key={row.id}>
                      <th scope="row" className="sticky left-0 max-w-[140px] truncate bg-panel p-2 text-left font-mono font-normal text-muted" title={row.id}>
                        {row.id}
                      </th>
                      {model.nodes.map((col) => {
                        const ms = model.matrix[row.id]?.[col.id];
                        const b = band(ms);
                        return (
                          <td key={col.id} className="p-1">
                            <span
                              className={cn("block min-w-[64px] rounded px-2 py-1.5 text-center font-mono tabular-nums", row.id === col.id && "text-faint")}
                              style={b ? { background: `${bandColor[b]}22`, color: bandColor[b] } : undefined}
                            >
                              {row.id === col.id ? "·" : formatMs(ms)}
                            </span>
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <aside className="panel h-fit p-5" aria-live="polite">
          {node ? (
            <>
              <p className="text-xs uppercase tracking-wider text-faint">Node</p>
              <h2 className="mt-1 break-all font-mono text-lg">{node.id}</h2>
              <div className="mt-2 flex flex-wrap gap-2">
                <StatusBadge tone={memberTone[node.state]}>{node.state}</StatusBadge>
                <StatusBadge tone="muted">{node.region || "no region"}</StatusBadge>
              </div>
              <dl className="mt-4 space-y-1.5 text-sm">
                <div className="flex justify-between gap-3">
                  <dt className="text-muted">Services owned</dt>
                  <dd>{node.services}</dd>
                </div>
                <div className="flex justify-between gap-3">
                  <dt className="text-muted">Version</dt>
                  <dd className="font-mono">{node.version ?? "—"}</dd>
                </div>
              </dl>
              {node.error && <p className="mt-3 text-xs text-warn">Its own view couldn&apos;t be read: {node.error}</p>}
              <h3 className="mt-5 text-sm font-medium">Latency to peers</h3>
              <ul className="mt-2 space-y-1.5">
                {model.nodes
                  .filter((n) => n.id !== node.id)
                  .map((n) => ({ n, ms: model.matrix[node.id]?.[n.id] ?? model.matrix[n.id]?.[node.id] }))
                  .sort((a, b) => (a.ms ?? Infinity) - (b.ms ?? Infinity))
                  .map(({ n, ms }) => (
                    <li key={n.id} className="flex items-center justify-between gap-3 text-sm">
                      <button className="min-w-0 truncate text-left font-mono text-muted hover:text-fg" onClick={() => setSelected(n.id)}>
                        {n.id}
                      </button>
                      <span className="font-mono tabular-nums" style={{ color: ms !== undefined ? bandColor[band(ms)!] : undefined }}>
                        {formatMs(ms)}
                      </span>
                    </li>
                  ))}
              </ul>
            </>
          ) : (
            <>
              <h2 className="font-medium">Select a node</h2>
              <p className="mt-2 text-sm text-muted">Click a node in the graph or on the map to see its latency to every peer.</p>
              <p className="mt-4 text-xs text-faint">Updated {relative(topology?.at)}. Nodes appear on the world map by region code (Fly.io, AWS, GCP, Azure) or their ATLAS_LOCATION.</p>
            </>
          )}
        </aside>
      </div>
    </>
  );
}
