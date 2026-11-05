"use client";

import Link from "next/link";
import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import { Pause, Play, RotateCw, Trash2 } from "lucide-react";
import { useCluster, type FeedItem } from "@/lib/cluster";
import { useConnection } from "@/lib/connection";
import { explain, type HistoryEntry } from "@/lib/api";
import { cn, describeEvent, relative, timestamp, type Tone } from "@/lib/format";
import { Chip, Empty, Notice, PageHeader, SearchInput, Skeleton, StatusBadge } from "@/components/console/ui";

const toneDot: Record<Tone, string> = { ok: "bg-ok", warn: "bg-warn", bad: "bg-bad", muted: "bg-faint", info: "bg-info" };

function LiveFeed() {
  const { feed, stream, streamError, reconnect, clearFeed } = useCluster();
  const [kind, setKind] = useState<"all" | "member" | "service">("all");
  const [q, setQ] = useState("");
  const [paused, setPaused] = useState<FeedItem[] | null>(null);
  const shown = paused ?? feed;
  const hidden = paused ? feed.length - paused.length : 0;

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return shown
      .filter((e) => kind === "all" || e.kind === kind)
      .map((e) => ({ e, d: describeEvent(e) }))
      .filter(({ e, d }) => !needle || d.text.toLowerCase().includes(needle) || e.type.includes(needle));
  }, [shown, kind, q]);

  return (
    <>
      {stream === "reconnecting" && <Notice tone="warn" title="Reconnecting to the event stream">Events will resume where they stopped; nothing is lost while the node keeps its backlog.</Notice>}
      {stream === "offline" && (
        <Notice tone="error" title="Live events are disconnected">
          {explain(streamError)}{" "}
          <button className="underline" onClick={reconnect}>
            Try again
          </button>
        </Notice>
      )}
      <div className="my-4 flex flex-wrap items-center gap-3">
        <SearchInput label="Filter events" value={q} onChange={setQ} placeholder="Filter by service, node or event type" />
        <div className="flex gap-2" role="group" aria-label="Event kind">
          {(["all", "member", "service"] as const).map((k) => (
            <Chip key={k} active={kind === k} onClick={() => setKind(k)}>
              {k === "all" ? "All" : k === "member" ? "Membership" : "Services"}
            </Chip>
          ))}
        </div>
        <div className="ml-auto flex gap-2">
          <button className="btn btn-outline !py-1.5 text-xs" onClick={() => setPaused((p) => (p ? null : feed))} aria-pressed={!!paused}>
            {paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />} {paused ? `Resume${hidden ? ` (${hidden} new)` : ""}` : "Pause"}
          </button>
          <button className="btn btn-outline !py-1.5 text-xs" onClick={() => (clearFeed(), setPaused(null))}>
            <Trash2 className="h-3.5 w-3.5" /> Clear
          </button>
        </div>
      </div>
      <div className="panel">
        {rows.length === 0 ? (
          <Empty
            title={feed.length ? "No events match" : "Waiting for events"}
            desc={feed.length ? "Change the filters to see more." : "Joins, failures, registrations and health changes appear here the moment they happen. Events from before you connected are in the History tab."}
          />
        ) : (
          <ol className="divide-y divide-line" aria-live={paused ? "off" : "polite"} aria-relevant="additions">
            {rows.map(({ e, d }) => (
              <li key={`${e.id}-${e.receivedAt}`} className="flex items-start gap-3 px-4 py-3 text-sm">
                <span className={cn("mt-1.5 h-2 w-2 shrink-0 rounded-full", toneDot[d.tone])} aria-hidden />
                <div className="min-w-0 flex-1">
                  <p className="break-words">
                    {e.kind === "service" && e.service ? (
                      <Link href={`/services/?id=${encodeURIComponent(e.service.id)}`} className="hover:underline">
                        {d.text}
                      </Link>
                    ) : (
                      d.text
                    )}
                  </p>
                  <p className="mt-0.5 font-mono text-xs text-faint">{e.type}</p>
                </div>
                <time className="shrink-0 font-mono text-xs text-faint" dateTime={e.at} title={timestamp(e.at)}>
                  {relative(e.at)}
                </time>
              </li>
            ))}
          </ol>
        )}
      </div>
    </>
  );
}

function HistoryLog() {
  const { client } = useConnection();
  const [name, setName] = useState("");
  const [applied, setApplied] = useState("");
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null);
  const [next, setNext] = useState<number | undefined>();
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (before?: number) => {
      if (!client) return;
      try {
        const page = await client.recentHistory(applied || undefined, before, 50);
        setEntries((e) => (before ? [...(e ?? []), ...page.entries] : page.entries));
        setNext(page.next_before);
        setError(null);
      } catch (err) {
        setError(explain(err));
      }
    },
    [client, applied],
  );
  useEffect(() => {
    setEntries(null);
    void load();
  }, [load]);

  const tone: Record<HistoryEntry["change"], Tone> = { registered: "ok", updated: "info", health: "warn", deregistered: "muted" };
  return (
    <>
      <form
        className="my-4 flex flex-wrap gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          setApplied(name.trim());
        }}
      >
        <SearchInput label="Service name" value={name} onChange={setName} placeholder="Only this service name (exact)" />
        <button className="btn btn-outline">Apply</button>
        <button type="button" className="btn btn-outline" onClick={() => load()} aria-label="Reload history">
          <RotateCw className="h-4 w-4" />
        </button>
      </form>
      <div className="panel">
        {error ? (
          <div className="p-4">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : entries === null ? (
          <div className="space-y-3 p-5">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-8" />
            ))}
          </div>
        ) : entries.length === 0 ? (
          <Empty title="No recorded changes" desc="This node keeps service history for ATLAS_HISTORY_RETENTION (7 days by default)." />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-sm">
              <thead className="border-b border-line text-left text-xs text-faint">
                <tr>
                  <th scope="col" className="px-4 py-3 font-normal">When</th>
                  <th scope="col" className="px-4 py-3 font-normal">Service</th>
                  <th scope="col" className="px-4 py-3 font-normal">Change</th>
                  <th scope="col" className="px-4 py-3 font-normal">Details</th>
                  <th scope="col" className="px-4 py-3 font-normal">Revision</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {entries.map((h) => (
                  <tr key={`${h.service_id}-${h.seq}`}>
                    <td className="px-4 py-3 text-muted" title={timestamp(h.at)}>{relative(h.at)}</td>
                    <td className="px-4 py-3">
                      <Link href={`/services/?id=${encodeURIComponent(h.service_id)}`} className="hover:underline">
                        <span className="font-medium">{h.name}</span> <span className="font-mono text-xs text-faint">{h.service_id}</span>
                      </Link>
                    </td>
                    <td className="px-4 py-3">
                      <StatusBadge tone={tone[h.change]}>{h.change}</StatusBadge>
                    </td>
                    <td className="px-4 py-3 text-muted">
                      {h.service ? (
                        <>
                          {h.service.address} · {h.service.health}
                          {h.service.draining ? " · draining" : ""}
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="px-4 py-3 font-mono tabular-nums text-muted">{h.revision}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {next && (
        <button className="btn btn-outline mt-4" onClick={() => load(next)}>
          Load older
        </button>
      )}
    </>
  );
}

function ActivityView() {
  const params = useSearchParams();
  const [tab, setTab] = useState<"live" | "history">(params.get("tab") === "history" ? "history" : "live");
  return (
    <>
      <PageHeader title="Activity" desc="Live membership and service events from this node, and the recorded history of every service change." />
      <div role="tablist" aria-label="Activity views" className="flex gap-1 border-b border-line">
        {(
          [
            ["live", "Live events"],
            ["history", "History"],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            role="tab"
            aria-selected={tab === id}
            onClick={() => setTab(id)}
            className={cn("-mb-px border-b-2 px-4 py-2 text-sm", tab === id ? "border-lime text-fg" : "border-transparent text-muted hover:text-fg")}
          >
            {label}
          </button>
        ))}
      </div>
      <div role="tabpanel">{tab === "live" ? <LiveFeed /> : <HistoryLog />}</div>
    </>
  );
}

export default function ActivityPage() {
  return (
    <Suspense>
      <ActivityView />
    </Suspense>
  );
}
