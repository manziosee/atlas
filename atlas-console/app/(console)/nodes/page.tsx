"use client";

import { useMemo, useState } from "react";
import { Trash2 } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { can, useConnection } from "@/lib/connection";
import { explain, type MemberState } from "@/lib/api";
import { memberTone, relative, timestamp } from "@/lib/format";
import { Chip, ConfirmDialog, Empty, Notice, PageHeader, SearchInput, Skeleton, StatusBadge } from "@/components/console/ui";
import { useToast } from "@/components/console/toast";

const states: MemberState[] = ["alive", "suspect", "dead", "left"];
const stateHelp: Record<MemberState, string> = {
  alive: "answering probes",
  suspect: "missed probes; may refute",
  dead: "confirmed failed",
  left: "left gracefully",
};

export default function NodesPage() {
  const { members, services, loading, lastUpdated, refresh } = useCluster();
  const { conn, client } = useConnection();
  const toast = useToast();
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<MemberState | "all">("all");
  const [evicting, setEvicting] = useState<string | null>(null);
  const admin = can(conn, "admin");

  const counts = useMemo(() => Object.fromEntries(states.map((s) => [s, members.filter((m) => m.state === s).length])) as Record<MemberState, number>, [members]);
  const owned = useMemo(() => {
    const map = new Map<string, number>();
    for (const s of services) map.set(s.node_id, (map.get(s.node_id) ?? 0) + 1);
    return map;
  }, [services]);
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return members
      .filter((m) => filter === "all" || m.state === filter)
      .filter((m) => !needle || [m.id, m.region, m.addr, m.api_addr ?? ""].some((v) => v.toLowerCase().includes(needle)))
      .sort((a, b) => states.indexOf(a.state) - states.indexOf(b.state) || a.id.localeCompare(b.id));
  }, [members, q, filter]);

  return (
    <>
      <PageHeader title="Nodes" desc="Cluster members in this node's view. Suspect nodes may still recover; dead and left nodes can be evicted by an admin." />
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <SearchInput label="Search nodes" value={q} onChange={setQ} placeholder="Search by ID, region or address" />
        <div className="flex flex-wrap gap-2" role="group" aria-label="Filter by state">
          <Chip active={filter === "all"} onClick={() => setFilter("all")}>
            All {members.length}
          </Chip>
          {states.map((s) => (
            <Chip key={s} active={filter === s} onClick={() => setFilter(s)}>
              {s} {counts[s]}
            </Chip>
          ))}
        </div>
      </div>

      <div className="panel overflow-hidden">
        {loading && !lastUpdated ? (
          <div className="space-y-3 p-5">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <Empty title="No nodes match" desc="Try a different search or state filter." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[860px] text-sm">
              <thead className="border-b border-line text-left text-xs text-faint">
                <tr>
                  <th scope="col" className="px-4 py-3 font-normal">Node</th>
                  <th scope="col" className="px-4 py-3 font-normal">State</th>
                  <th scope="col" className="px-4 py-3 font-normal">Region</th>
                  <th scope="col" className="px-4 py-3 font-normal">Gossip address</th>
                  <th scope="col" className="px-4 py-3 font-normal">API</th>
                  <th scope="col" className="px-4 py-3 font-normal">Incarnation</th>
                  <th scope="col" className="px-4 py-3 font-normal">In this state</th>
                  <th scope="col" className="px-4 py-3 font-normal">Services</th>
                  {admin && <th scope="col" className="px-4 py-3 font-normal"><span className="sr-only">Actions</span></th>}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {rows.map((m) => (
                  <tr key={m.id} className={m.state === "dead" ? "bg-bad/[0.04]" : m.state === "suspect" ? "bg-warn/[0.04]" : undefined}>
                    <td className="px-4 py-3 font-mono">
                      {m.id}
                      {m.id === conn?.info.node.id && <span className="ml-2 rounded bg-lime/15 px-1.5 py-0.5 font-sans text-[0.68rem] text-lime">this node</span>}
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge tone={memberTone[m.state]}>{m.state}</StatusBadge>
                      <span className="sr-only"> — {stateHelp[m.state]}</span>
                    </td>
                    <td className="px-4 py-3 font-mono">{m.region || "—"}</td>
                    <td className="max-w-[220px] truncate px-4 py-3 font-mono text-muted" title={m.addr}>{m.addr}</td>
                    <td className="max-w-[220px] truncate px-4 py-3 font-mono text-muted" title={m.api_addr}>{m.api_addr || "—"}</td>
                    <td className="px-4 py-3 font-mono tabular-nums text-muted">{m.incarnation}</td>
                    <td className="px-4 py-3 text-muted" title={timestamp(m.since)}>{relative(m.since).replace(" ago", "")}</td>
                    <td className="px-4 py-3 tabular-nums">{owned.get(m.id) ?? 0}</td>
                    {admin && (
                      <td className="px-4 py-3 text-right">
                        {m.state !== "alive" && (
                          <button className="btn btn-danger !px-2.5 !py-1 text-xs" onClick={() => setEvicting(m.id)}>
                            <Trash2 className="h-3.5 w-3.5" /> Evict
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <div className="mt-4 flex flex-wrap gap-x-5 gap-y-1 text-xs text-muted">
        {states.map((s) => (
          <span key={s}>
            <span className="font-medium text-fg">{s}</span> — {stateHelp[s]}
          </span>
        ))}
      </div>
      {!admin && conn?.mode !== "demo" && (
        <div className="mt-6">
          <Notice tone="info">Evicting failed nodes needs an admin token.</Notice>
        </div>
      )}

      <ConfirmDialog
        open={evicting !== null}
        onOpenChange={(o) => !o && setEvicting(null)}
        title="Evict this node?"
        confirmLabel="Evict node"
        typeToConfirm={evicting ?? undefined}
        onConfirm={async () => {
          if (!client || !evicting) return;
          try {
            const result = await client.evict(evicting);
            toast("ok", `Evicted ${result.node_id}; ${result.services_removed} services removed.`);
            await refresh();
          } catch (err) {
            toast("error", explain(err));
          }
        }}
      >
        <p>
          <span className="font-mono text-fg">{evicting}</span> will be removed from the member list and its{" "}
          <span className="text-fg">{evicting ? owned.get(evicting) ?? 0 : 0} services</span> deleted from the registry on every node.
        </p>
        <p>Only do this for nodes that are gone for good. A node that comes back will rejoin and must register its services again.</p>
      </ConfirmDialog>
    </>
  );
}
