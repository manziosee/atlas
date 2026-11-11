"use client";

import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { HeartPulse, Loader2, Pause, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { can, useConnection } from "@/lib/connection";
import { explain, type Health, type HistoryEntry, type Service } from "@/lib/api";
import { healthTone, relative, timestamp } from "@/lib/format";
import { isSourceTag, sourceLabel, sourceOf, type SourceKind } from "@/lib/source";
import { Chip, ConfirmDialog, Empty, Modal, Notice, PageHeader, SearchInput, Skeleton, StatusBadge, Tag } from "@/components/console/ui";
import { ServiceForm } from "@/components/console/service-form";
import { useToast } from "@/components/console/toast";

const healths: Health[] = ["healthy", "unknown", "unhealthy"];

function ServicesView() {
  const { services, members, loading, lastUpdated, applyService, removeService } = useCluster();
  const { conn, client } = useConnection();
  const toast = useToast();
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const selectedId = params.get("id");
  const [q, setQ] = useState(params.get("q") ?? "");
  const [health, setHealth] = useState<Health | "all">("all");
  const [region, setRegion] = useState("all");
  const [onlyDraining, setOnlyDraining] = useState(false);
  const [source, setSource] = useState<SourceKind | "all">("all");
  const [registering, setRegistering] = useState(false);
  const writable = can(conn, "write");

  const select = useCallback(
    (id: string | null) => {
      const next = new URLSearchParams(params.toString());
      if (id) next.set("id", id);
      else next.delete("id");
      router.replace(`${pathname}${next.size ? `?${next}` : ""}`, { scroll: false });
    },
    [params, pathname, router],
  );

  const regions = useMemo(() => [...new Set(services.map((s) => s.region).filter(Boolean))].sort(), [services]);
  const aliveNodes = useMemo(() => new Set(members.filter((m) => m.state === "alive").map((m) => m.id)), [members]);
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return services
      .filter((s) => health === "all" || s.health === health)
      .filter((s) => region === "all" || s.region === region)
      .filter((s) => !onlyDraining || s.draining)
      .filter((s) => source === "all" || sourceOf(s).kind === source)
      .filter((s) => !needle || [s.name, s.id, s.address, s.version ?? "", s.node_id, ...(s.tags ?? [])].some((v) => v.toLowerCase().includes(needle)))
      .sort((a, b) => a.name.localeCompare(b.name) || a.region.localeCompare(b.region) || a.id.localeCompare(b.id));
  }, [services, q, health, region, onlyDraining, source]);
  const selected = services.find((s) => s.id === selectedId) ?? null;

  return (
    <>
      <PageHeader title="Services" desc="Every registered instance in this node's replica. Select one for its health settings, history and actions.">
        {writable ? (
          <button className="btn btn-lime" onClick={() => setRegistering(true)}>
            <Plus className="h-4 w-4" /> Register service
          </button>
        ) : (
          <span className="rounded-full border border-line-strong px-3 py-1.5 text-xs text-muted">{conn?.mode === "demo" ? "Read-only demo" : "Read-only token"}</span>
        )}
      </PageHeader>

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <SearchInput label="Search services" value={q} onChange={setQ} placeholder="Search name, ID, address, tag, node or version" />
        <label className="text-sm">
          <span className="sr-only">Region</span>
          <select className="field w-auto" value={region} onChange={(e) => setRegion(e.target.value)}>
            <option value="all">All regions</option>
            {regions.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
      </div>
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label="Filter by health">
        <Chip active={health === "all"} onClick={() => setHealth("all")}>
          All {services.length}
        </Chip>
        {healths.map((h) => (
          <Chip key={h} active={health === h} onClick={() => setHealth(h)}>
            {h} {services.filter((s) => s.health === h).length}
          </Chip>
        ))}
        <Chip active={onlyDraining} onClick={() => setOnlyDraining((d) => !d)}>
          draining {services.filter((s) => s.draining).length}
        </Chip>
        <span className="mx-1 w-px self-stretch bg-line" aria-hidden />
        {(["kubernetes", "docker", "manual"] as const).map((k) => {
          const n = services.filter((s) => sourceOf(s).kind === k).length;
          return n ? (
            <Chip key={k} active={source === k} onClick={() => setSource((cur) => (cur === k ? "all" : k))}>
              {sourceLabel[k]} {n}
            </Chip>
          ) : null;
        })}
      </div>

      <div className="panel overflow-hidden">
        {loading && !lastUpdated ? (
          <div className="space-y-3 p-5">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          services.length === 0 ? (
            <Empty title="No services registered yet" desc="Register one here, with a client library, over the API, or by labelling a Docker container with atlas.name." />
          ) : (
            <Empty title="No services match" desc="Clear the search or filters to see everything." />
          )
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[1080px] text-sm">
              <thead className="border-b border-line text-left text-xs text-faint">
                <tr>
                  <th scope="col" className="px-4 py-3 font-normal">Service</th>
                  <th scope="col" className="px-4 py-3 font-normal">Health</th>
                  <th scope="col" className="px-4 py-3 font-normal">Address</th>
                  <th scope="col" className="px-4 py-3 font-normal">Region</th>
                  <th scope="col" className="px-4 py-3 font-normal">Version</th>
                  <th scope="col" className="px-4 py-3 font-normal">Source</th>
                  <th scope="col" className="px-4 py-3 font-normal">Tags</th>
                  <th scope="col" className="px-4 py-3 font-normal">Owner node</th>
                  <th scope="col" className="px-4 py-3 font-normal">Updated</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {rows.map((s) => (
                  <tr key={s.id} className="cursor-pointer hover:bg-white/[0.03]" onClick={() => select(s.id)}>
                    <td className="px-4 py-3">
                      <button className="text-left" onClick={(e) => (e.stopPropagation(), select(s.id))}>
                        <span className="block font-medium">{s.name}</span>
                        <span className="block max-w-[240px] truncate font-mono text-xs text-faint">{s.id}</span>
                      </button>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex flex-wrap gap-1.5">
                        <StatusBadge tone={healthTone[s.health]}>{s.health}</StatusBadge>
                        {s.draining && <StatusBadge tone="info">draining</StatusBadge>}
                        {(s.traffic_weight ?? 1) !== 1 && <StatusBadge tone="muted">weight {s.traffic_weight}</StatusBadge>}
                      </div>
                    </td>
                    <td className="max-w-[220px] truncate px-4 py-3 font-mono text-muted" title={s.address}>{s.address}</td>
                    <td className="px-4 py-3 font-mono">{s.region || "—"}</td>
                    <td className="px-4 py-3 font-mono text-muted">{s.version || "—"}</td>
                    <td className="px-4 py-3">
                      <SourceBadge service={s} />
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex max-w-[220px] flex-wrap gap-1">{(s.tags ?? []).filter((t) => !isSourceTag(t)).slice(0, 4).map((t) => <Tag key={t}>{t}</Tag>)}</div>
                    </td>
                    <td className="px-4 py-3 font-mono text-muted">
                      {s.node_id}
                      {!aliveNodes.has(s.node_id) && <span className="ml-1.5 text-xs text-warn">(owner not alive)</span>}
                    </td>
                    <td className="px-4 py-3 text-muted">{relative(s.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <p className="mt-3 text-xs text-faint">
        Showing {rows.length} of {services.length} instances.
      </p>

      <Modal open={registering} onOpenChange={setRegistering} title="Register a service" description="The node you're connected to will own it and run its health checks.">
        <ServiceForm
          onCancel={() => setRegistering(false)}
          onSubmit={async (spec) => {
            const created = await client!.register(spec);
            applyService(created);
            setRegistering(false);
            toast("ok", `Registered ${created.name} (${created.id}).`);
            select(created.id);
          }}
        />
      </Modal>

      <Modal
        side
        open={!!selectedId}
        onOpenChange={(o) => !o && select(null)}
        title={selected ? selected.name : "Service"}
        description={selected ? <span className="font-mono">{selected.id}</span> : undefined}
      >
        {selected ? (
          <ServiceDetail key={selected.id} service={selected} writable={writable} ownerAlive={aliveNodes.has(selected.node_id)} onChanged={applyService} onRemoved={(id) => (removeService(id), select(null))} />
        ) : loading ? (
          <Loader2 className="h-5 w-5 animate-spin text-muted" />
        ) : (
          <Notice tone="warn">This service isn&apos;t in the registry any more. It may have expired or been deregistered.</Notice>
        )}
      </Modal>
    </>
  );
}

function SourceBadge({ service, detailed = false }: { service: Service; detailed?: boolean }) {
  const src = sourceOf(service);
  const tone = src.kind === "kubernetes" ? "text-info border-info/35 bg-info/10" : src.kind === "docker" ? "text-teal border-teal/35 bg-teal/10" : "text-muted border-line-strong";
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span className={`rounded-full border px-2 py-0.5 text-xs ${tone}`}>{sourceLabel[src.kind]}</span>
      {src.owner && <span className="font-mono text-xs text-muted">{src.owner}</span>}
      {detailed && src.namespace && <span className="font-mono text-xs text-muted">ns/{src.namespace}</span>}
      {detailed && src.workload && <span className="font-mono text-xs text-muted">{src.workload}</span>}
    </span>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[9rem_1fr] gap-3 py-2 text-sm">
      <dt className="text-muted">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

function ServiceDetail({
  service: s,
  writable,
  ownerAlive,
  onChanged,
  onRemoved,
}: {
  service: Service;
  writable: boolean;
  ownerAlive: boolean;
  onChanged: (s: Service) => void;
  onRemoved: (id: string) => void;
}) {
  const { client, conn } = useConnection();
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [history, setHistory] = useState<HistoryEntry[] | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [nextBefore, setNextBefore] = useState<number | undefined>();

  const loadHistory = useCallback(
    async (before?: number) => {
      if (!client) return;
      try {
        const page = await client.history(s.id, before, 20);
        setHistory((h) => (before ? [...(h ?? []), ...page.entries] : page.entries));
        setNextBefore(page.next_before);
        setHistoryError(null);
      } catch (err) {
        setHistoryError(explain(err));
      }
    },
    [client, s.id],
  );
  useEffect(() => {
    void loadHistory();
  }, [loadHistory, s.revision]);

  const act = async (name: string, fn: () => Promise<Service>, done: string) => {
    setBusy(name);
    try {
      onChanged(await fn());
      toast("ok", done);
    } catch (err) {
      toast("error", explain(err));
    } finally {
      setBusy(null);
    }
  };

  if (editing) {
    return (
      <ServiceForm
        service={s}
        onCancel={() => setEditing(false)}
        onSubmit={async (_spec, patch) => {
          const updated = await client!.update(s.id, patch, s.revision);
          onChanged(updated);
          setEditing(false);
          toast("ok", `Saved ${updated.name} (revision ${updated.revision}).`);
        }}
      />
    );
  }

  return (
    <div>
      <div className="flex flex-wrap gap-2">
        <StatusBadge tone={healthTone[s.health]}>{s.health}</StatusBadge>
        {s.draining && <StatusBadge tone="info">draining</StatusBadge>}
        {!ownerAlive && <StatusBadge tone="warn">owner not alive</StatusBadge>}
      </div>

      {writable ? (
        <div className="mt-5 flex flex-wrap gap-2">
          <button className="btn btn-outline !py-1.5 text-xs" onClick={() => setEditing(true)}>
            <Pencil className="h-3.5 w-3.5" /> Edit
          </button>
          <button
            className="btn btn-outline !py-1.5 text-xs"
            disabled={busy !== null}
            onClick={() => act("drain", () => client!.setDraining(s.id, !s.draining), s.draining ? `${s.name} is back in rotation.` : `${s.name} is draining; lookups skip it.`)}
          >
            {busy === "drain" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : s.draining ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
            {s.draining ? "Undrain" : "Drain"}
          </button>
          {!!s.ttl_seconds && (
            <button className="btn btn-outline !py-1.5 text-xs" disabled={busy !== null} onClick={() => act("beat", () => client!.heartbeat(s.id), `Heartbeat sent; ${s.name} is healthy for ${s.ttl_seconds}s.`)}>
              {busy === "beat" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <HeartPulse className="h-3.5 w-3.5" />} Heartbeat
            </button>
          )}
          <button className="btn btn-danger !py-1.5 text-xs" onClick={() => setConfirmDelete(true)}>
            <Trash2 className="h-3.5 w-3.5" /> Deregister
          </button>
        </div>
      ) : (
        <p className="mt-4 text-xs text-muted">{conn?.mode === "demo" ? "The demo is read-only." : "Changing services needs a token with the write scope."}</p>
      )}

      <dl className="mt-5 divide-y divide-line border-y border-line">
        <Row label="Address">
          <span className="font-mono">{s.address}</span>
        </Row>
        <Row label="Region">{s.region || "—"}</Row>
        <Row label="Version">{s.version || "—"}</Row>
        <Row label="Traffic weight">
          {s.traffic_weight ?? 1}
          <span className="ml-2 text-xs text-muted">relative share among healthy instances of {s.name}</span>
        </Row>
        <Row label="Tags">
          <div className="flex flex-wrap gap-1">{s.tags?.length ? s.tags.map((t) => <Tag key={t}>{t}</Tag>) : "—"}</div>
        </Row>
        <Row label="Source">
          <SourceBadge service={s} detailed />
        </Row>
        <Row label="Owner node">
          <span className="font-mono">{s.node_id}</span>
        </Row>
        {s.health_check ? (
          <>
            <Row label="Health check">
              <span className="font-mono">{s.health_check}</span>
            </Row>
            <Row label="Check every">{s.check_interval_seconds ? `${s.check_interval_seconds}s` : "node default"}</Row>
            <Row label="Thresholds">
              {s.unhealthy_threshold ?? 3} failures to unhealthy · {s.healthy_threshold ?? 2} passes to recover
            </Row>
            <Row label="Last checked">{s.checked_at ? `${relative(s.checked_at)} (${timestamp(s.checked_at)})` : "only the owner records this"}</Row>
          </>
        ) : s.ttl_seconds ? (
          <>
            <Row label="TTL">
              {s.ttl_seconds}s — unhealthy after one missed TTL, removed after three
            </Row>
            <Row label="Last heartbeat">{s.heartbeat_at ? `${relative(s.heartbeat_at)} (${timestamp(s.heartbeat_at)})` : "only the owner records this"}</Row>
          </>
        ) : (
          <Row label="Health">No check or TTL: health stays unknown.</Row>
        )}
        <Row label="Revision">{s.revision}</Row>
        <Row label="Updated">
          {relative(s.updated_at)} ({timestamp(s.updated_at)})
        </Row>
      </dl>

      <h3 className="mt-6 font-medium">Change history</h3>
      {historyError ? (
        <p className="mt-2 text-sm text-muted">{historyError}</p>
      ) : history === null ? (
        <div className="mt-3 space-y-2">
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      ) : history.length === 0 ? (
        <p className="mt-2 text-sm text-muted">No changes recorded on this node yet.</p>
      ) : (
        <ol className="mt-3 space-y-3 border-l border-line pl-4">
          {history.map((h) => (
            <li key={h.seq} className="relative text-sm">
              <span className="absolute -left-[1.3rem] top-1.5 h-2 w-2 rounded-full bg-teal" aria-hidden />
              <p>
                <span className="font-medium capitalize">{h.change}</span>
                {h.change === "health" && h.service && <span className="text-muted"> → {h.service.health}</span>}
                {h.change === "updated" && h.service?.draining !== undefined && <span className="text-muted">{h.service.draining ? " · draining" : ""}</span>}
                <span className="text-muted"> · revision {h.revision}</span>
              </p>
              <p className="text-xs text-faint">
                {relative(h.at)} · {timestamp(h.at)}
              </p>
            </li>
          ))}
        </ol>
      )}
      {nextBefore && (
        <button className="btn btn-outline mt-4 !py-1.5 text-xs" onClick={() => loadHistory(nextBefore)}>
          Load older changes
        </button>
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Deregister ${s.name}?`}
        confirmLabel="Deregister"
        typeToConfirm={s.id}
        onConfirm={async () => {
          try {
            await client!.deregister(s.id);
            toast("ok", `Deregistered ${s.name} (${s.id}).`);
            onRemoved(s.id);
          } catch (err) {
            toast("error", explain(err));
          }
        }}
      >
        <p>Lookups, DNS and clients stop returning this instance on every node. If the service is still running and registers itself again, it will come back.</p>
        {s.ttl_seconds ? <p>To take it out of rotation temporarily, drain it instead.</p> : null}
      </ConfirmDialog>
    </div>
  );
}

export default function ServicesPage() {
  return (
    <Suspense>
      <ServicesView />
    </Suspense>
  );
}
