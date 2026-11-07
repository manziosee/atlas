"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BarChart3, Loader2, RefreshCw } from "lucide-react";
import { Notice, PageHeader, Skeleton, Stat, StatusBadge } from "@/components/console/ui";
import { explain, type AdminAnalytics } from "@/lib/api";
import { useCluster } from "@/lib/cluster";
import { can, useConnection } from "@/lib/connection";
import { timestamp } from "@/lib/format";
import { ConsoleLoading } from "@/components/console/shell";
import { useRouter } from "next/navigation";

export default function AdminAnalyticsPage() {
  const { conn, client } = useConnection();
  const { members, services, loading: clusterLoading, error: clusterError, refresh } = useCluster();
  const router = useRouter();
  const admin = can(conn, "admin");
  const [rangeDays, setRangeDays] = useState(30);
  const [report, setReport] = useState<AdminAnalytics | null>(null);
  const [reportError, setReportError] = useState<string | null>(null);
  const [reportLoading, setReportLoading] = useState(true);
  const requestSequence = useRef(0);

  const loadReport = useCallback(async () => {
    if (!client || !admin) return;
    const requestId = ++requestSequence.current;
    setReportLoading(true);
    try {
      const next = await client.analytics(rangeDays);
      if (requestId === requestSequence.current) {
        setReport(next);
        setReportError(null);
      }
    } catch (error) {
      if (requestId === requestSequence.current) setReportError(explain(error));
    } finally {
      if (requestId === requestSequence.current) setReportLoading(false);
    }
  }, [admin, client, rangeDays]);

  useEffect(() => {
    if (conn && !admin) router.replace("/dashboard/");
  }, [admin, conn, router]);

  useEffect(() => {
    if (admin) void loadReport();
    return () => {
      requestSequence.current++;
    };
  }, [admin, loadReport]);

  const activeNodes = members.filter((member) => member.state === "alive").length;
  const unhealthyServices = services.filter((service) => service.health === "unhealthy");
  const unhealthyNodes = members.filter((member) => member.state === "suspect" || member.state === "dead");
  const today = report?.daily.find((day) => day.date === new Date().toISOString().slice(0, 10));
  const todayVisitors = today?.unique_visitors ?? 0;
  const anyVisitorCapReached = report?.daily.some((day) => day.unique_visitors_capped) ?? false;
  const totalPageViews = useMemo(() => report?.daily.reduce((total, day) => total + day.page_views, 0) ?? 0, [report]);
  const maxViews = Math.max(1, ...(report?.daily.map((day) => day.page_views) ?? [0]));

  if (!admin) return <ConsoleLoading />;

  return (
    <>
      <PageHeader
        title="Usage & reliability"
        desc="Private operator dashboard. Page views are recorded by this deployment's analytics backend; API metrics combine reachable nodes, and node/service health is live."
      >
        <label className="flex items-center gap-2 text-sm text-muted">
          Range
          <select className="field w-auto py-2" value={rangeDays} onChange={(event) => setRangeDays(Number(event.target.value))}>
            <option value={7}>7 days</option>
            <option value={30}>30 days</option>
            <option value={90}>90 days</option>
          </select>
        </label>
        <Link href="/admin/" className="btn btn-quiet">Admin tools</Link>
        <button
          type="button"
          className="btn btn-quiet"
          disabled={reportLoading}
          onClick={() => {
            void Promise.all([loadReport(), refresh()]);
          }}
        >
          {reportLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
          Refresh
        </button>
      </PageHeader>

      <div className="mb-6">
        <Notice tone="info" title="Privacy-limited usage reporting">
          The site stores page totals and daily visitor summaries for 90 days. Estimated unique-browser counts are deduplicated across reachable nodes, have about 3% relative error, and are capped at {(10_000).toLocaleString()} recorded visitors per node per day. Counts may be underreported when a node reaches that cap. Raw browser IDs, names, IPs, query strings, and device fingerprints are not stored. API request counters reset when a node restarts.
        </Notice>
      </div>

      {reportError && (
        <div className="mb-6">
          <Notice tone="error" title="Couldn't load usage analytics">{reportError}</Notice>
        </div>
      )}
      {clusterError && (
        <div className="mb-6">
          <Notice tone="error" title="Couldn't load live cluster health">{explain(clusterError)}</Notice>
        </div>
      )}
      {!!report?.node_errors.length && (
        <div className="mb-6">
          <Notice tone="warn" title={`Partial analytics · ${report.nodes_included} nodes included`}>
            {report.node_errors.map((node) => `${node.node_id}: ${node.error}`).join("; ")}
          </Notice>
        </div>
      )}
      {anyVisitorCapReached && (
        <div className="mb-6">
          <Notice tone="warn" title="Visitor collection cap reached">
            At least one node reached the daily visitor-storage cap during this range. Unique-browser estimates for capped days may be lower than actual traffic.
          </Notice>
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        {reportLoading && !report ? (
          Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-[118px]" />)
        ) : (
          <>
            <Stat label="Estimated unique browsers today" value={todayVisitors} hint={today?.unique_visitors_capped ? "Visitor storage cap reached on at least one node" : "Approximate cluster-wide estimate"} />
            <Stat label={`Page views · ${report?.days ?? rangeDays} days`} value={totalPageViews} hint="Aggregated by page" />
            <Stat label="API requests" value={report?.api_requests_since_start ?? 0} hint="Across included nodes since each started" />
            <Stat
              label="Live Atlas nodes"
              value={clusterLoading ? "…" : activeNodes}
              tone={unhealthyNodes.length ? "warn" : "ok"}
              hint={`${members.length} known · ${unhealthyNodes.length} need attention`}
            />
            <Stat
              label="Service instances"
              value={clusterLoading ? "…" : services.length}
              tone={unhealthyServices.length ? "bad" : "ok"}
              hint={`${unhealthyServices.length} unhealthy`}
            />
          </>
        )}
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-3">
        <section className="panel p-5 xl:col-span-2" aria-labelledby="traffic-title">
          <div className="flex items-center gap-2">
            <BarChart3 className="h-4 w-4 text-lime" aria-hidden />
            <h2 id="traffic-title" className="font-medium">Daily page views · last {report?.days ?? rangeDays} days</h2>
          </div>
          {reportLoading && !report ? (
            <Skeleton className="mt-5 h-36" />
          ) : report?.daily.length ? (
            <>
              <div className="mt-5 flex h-36 items-end gap-1" role="list" aria-label="Daily page views">
                {report.daily.map((day) => (
                  <div
                    key={day.date}
                    role="listitem"
                    aria-label={`${day.date}: ${day.page_views} page views, about ${day.unique_visitors} unique browsers${day.unique_visitors_capped ? ", visitor storage capped" : ""}`}
                    title={`${day.date}: ${day.page_views} views · about ${day.unique_visitors} unique browsers${day.unique_visitors_capped ? " · visitor storage capped" : ""}`}
                    className="min-w-0 flex-1 rounded-t bg-lime/70 transition-colors hover:bg-lime"
                    style={{ height: day.page_views ? `${Math.max(3, (day.page_views / maxViews) * 100)}%` : "0%" }}
                  />
                ))}
              </div>
              <div className="mt-2 flex justify-between text-[0.68rem] text-faint">
                <span>{report.daily[0]?.date}</span>
                <span>{report.daily.at(-1)?.date}</span>
              </div>
            </>
          ) : (
            <p className="mt-5 text-sm text-muted">No page views have been recorded yet.</p>
          )}
        </section>

        <section className="panel p-5" aria-labelledby="pages-title">
          <h2 id="pages-title" className="font-medium">Most visited pages · {report?.days ?? rangeDays} days</h2>
          {!report?.pages.length ? (
            <p className="mt-4 text-sm text-muted">Page totals will appear after the first visit.</p>
          ) : (
            <ol className="mt-4 space-y-3">
              {report.pages.slice(0, 8).map((page) => (
                <li key={page.page} className="flex items-center justify-between gap-3 text-sm">
                  <span className="truncate font-mono text-muted">{page.page}</span>
                  <span className="shrink-0 font-mono tabular-nums">{page.page_views}</span>
                </li>
              ))}
            </ol>
          )}
          {report && (
            <p className="mt-5 border-t border-line pt-3 text-xs text-muted">
              {report.api_errors_since_start} API errors (5xx) · {report.api_requests_since_start} API requests since each node started
            </p>
          )}
        </section>
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-2">
        <section className="panel p-5" aria-labelledby="service-health-title">
          <div className="flex items-center justify-between gap-3">
            <h2 id="service-health-title" className="font-medium">Service health</h2>
            <div className="flex gap-2">
              <StatusBadge tone="ok">{services.filter((service) => service.health === "healthy").length} healthy</StatusBadge>
              <StatusBadge tone="bad">{unhealthyServices.length} unhealthy</StatusBadge>
            </div>
          </div>
          {clusterLoading ? (
            <div className="mt-4 space-y-2"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : unhealthyServices.length === 0 ? (
            <p className="mt-4 text-sm text-muted">No unhealthy services in this cluster.</p>
          ) : (
            <ul className="mt-4 divide-y divide-line">
              {unhealthyServices.slice(0, 10).map((service) => (
                <li key={service.id} className="flex flex-wrap items-center justify-between gap-2 py-3 text-sm">
                  <div className="min-w-0">
                    <Link href={`/services/?id=${encodeURIComponent(service.id)}`} className="block truncate font-medium hover:text-lime">
                      {service.name} <span className="font-mono text-muted">({service.id})</span>
                    </Link>
                    <p className="mt-1 text-xs text-muted">
                      {service.address} · Node {service.node_id} · {service.region || "no region"}
                      {service.checked_at ? ` · checked ${timestamp(service.checked_at)}` : ""}
                    </p>
                    {service.health_check && <p className="mt-1 break-all font-mono text-xs text-faint">Check: {service.health_check}</p>}
                  </div>
                  <StatusBadge tone="bad">unhealthy</StatusBadge>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="panel p-5" aria-labelledby="node-health-title">
          <div className="flex items-center justify-between gap-3">
            <h2 id="node-health-title" className="font-medium">Node health</h2>
            <StatusBadge tone={unhealthyNodes.length ? "warn" : "ok"}>{unhealthyNodes.length} need attention</StatusBadge>
          </div>
          {clusterLoading ? (
            <div className="mt-4 space-y-2"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : unhealthyNodes.length === 0 ? (
            <p className="mt-4 text-sm text-muted">All known nodes are alive.</p>
          ) : (
            <ul className="mt-4 divide-y divide-line">
              {unhealthyNodes.map((member) => (
                <li key={member.id} className="flex items-center justify-between gap-3 py-3 text-sm">
                  <span className="font-mono">{member.id}</span>
                  <StatusBadge tone="warn">{member.state}</StatusBadge>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <section className="panel mt-6 p-5" aria-labelledby="api-routes-title">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="api-routes-title" className="font-medium">HTTP API by route</h2>
            <p className="mt-1 text-xs text-muted">Aggregated counters since each included node started; 5xx responses are highlighted.</p>
          </div>
          {report && <StatusBadge tone={report.api_errors_since_start ? "bad" : "ok"}>{report.api_errors_since_start} server errors</StatusBadge>}
        </div>
        {!report?.api_requests_by_route.length ? (
          <p className="mt-4 text-sm text-muted">No HTTP requests have been recorded yet.</p>
        ) : (
          <div className="mt-4 overflow-x-auto">
            <table className="w-full min-w-[480px] text-sm">
              <thead className="border-b border-line text-left text-xs text-faint">
                <tr>
                  <th scope="col" className="py-2 pr-4 font-normal">Route</th>
                  <th scope="col" className="py-2 pr-4 font-normal">Requests</th>
                  <th scope="col" className="py-2 font-normal">5xx errors</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {report.api_requests_by_route.slice(0, 10).map((route) => (
                  <tr key={route.route}>
                    <td className="py-2.5 pr-4 font-mono text-muted">{route.route}</td>
                    <td className="py-2.5 pr-4 font-mono tabular-nums">{route.count}</td>
                    <td className="py-2.5">
                      <StatusBadge tone={route.errors ? "bad" : "ok"}>{route.errors}</StatusBadge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
