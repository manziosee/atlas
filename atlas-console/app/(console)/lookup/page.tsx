"use client";

import { useMemo, useState } from "react";
import { Loader2, Search } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { useConnection } from "@/lib/connection";
import { AtlasError, explain, type LookupOptions, type Service } from "@/lib/api";
import { cn, healthTone } from "@/lib/format";
import { CodeBlock } from "@/components/copy-button";
import { Empty, Field, Notice, PageHeader, StatusBadge, Tag } from "@/components/console/ui";

type Verdict = { service: Service; eligible: boolean; reason: string };

/** Why each instance of the service is or isn't a candidate, in the node's rule order. */
function explainCandidates(services: Service[], aliveNodes: Set<string>, name: string, opts: LookupOptions): Verdict[] {
  return services
    .filter((s) => s.name === name)
    .map((s) => {
      if (s.draining) return { service: s, eligible: false, reason: "draining — receives no traffic" };
      if (s.health === "unhealthy") return { service: s, eligible: false, reason: "unhealthy" };
      const missing = (opts.tags ?? []).filter((t) => !(s.tags ?? []).includes(t));
      if (missing.length) return { service: s, eligible: false, reason: `missing tag ${missing.join(", ")}` };
      if (!aliveNodes.has(s.node_id)) {
        if (opts.stale === "exclude") return { service: s, eligible: false, reason: "owner node not alive (stale=exclude)" };
        return { service: s, eligible: true, reason: opts.stale === "include" ? "owner not alive, included (stale=include)" : "owner not alive — used only if no live-owner instance (prefer-live)" };
      }
      const regional = opts.region && s.region !== opts.region ? `outside ${opts.region} — fallback only` : opts.region ? `in ${opts.region}` : "any region";
      return { service: s, eligible: true, reason: s.health === "healthy" ? `healthy, ${regional}` : `health unknown — used only if none is healthy; ${regional}` };
    })
    .sort((a, b) => Number(b.eligible) - Number(a.eligible));
}

export default function LookupPage() {
  const { services, members } = useCluster();
  const { client, conn } = useConnection();
  const [name, setName] = useState("");
  const [region, setRegion] = useState("");
  const [tags, setTags] = useState("");
  const [key, setKey] = useState("");
  const [stale, setStale] = useState<LookupOptions["stale"]>("");
  const [latency, setLatency] = useState<LookupOptions["latency"]>("");
  const [tolerance, setTolerance] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ pick?: Service; error?: string; code?: string; opts: LookupOptions; name: string } | null>(null);

  const names = useMemo(() => [...new Set(services.map((s) => s.name))].sort(), [services]);
  const regions = useMemo(() => [...new Set([...services.map((s) => s.region), ...members.map((m) => m.region)].filter(Boolean))].sort(), [services, members]);
  const aliveNodes = useMemo(() => new Set(members.filter((m) => m.state === "alive").map((m) => m.id)), [members]);

  const run = async () => {
    if (!client || !name.trim()) return;
    const opts: LookupOptions = {
      region: region || undefined,
      key: key || undefined,
      tags: tags.split(",").map((t) => t.trim()).filter(Boolean),
      stale,
      latency,
      tolerance: latency === "nearest" && tolerance ? tolerance : undefined,
    };
    setBusy(true);
    try {
      const pick = await client.lookup(name.trim(), opts);
      setResult({ pick, opts, name: name.trim() });
    } catch (err) {
      setResult({ error: explain(err), code: err instanceof AtlasError ? err.code : undefined, opts, name: name.trim() });
    } finally {
      setBusy(false);
    }
  };

  const verdicts = result ? explainCandidates(services, aliveNodes, result.name, result.opts) : [];
  const curl = result
    ? (() => {
        const p = new URLSearchParams();
        if (result.opts.region) p.set("region", result.opts.region);
        if (result.opts.key) p.set("key", result.opts.key);
        for (const t of result.opts.tags ?? []) p.append("tag", t);
        if (result.opts.stale) p.set("stale", result.opts.stale);
        if (result.opts.latency) p.set("latency", result.opts.latency);
        if (result.opts.tolerance) p.set("tolerance", result.opts.tolerance);
        const base = conn?.mode === "demo" ? "https://atlas-edge.fly.dev" : conn?.url;
        return `curl -H "Authorization: Bearer $ATLAS_TOKEN" \\\n  "${base}/v1/lookup/${encodeURIComponent(result.name)}${p.size ? `?${p}` : ""}"`;
      })()
    : "";

  return (
    <>
      <PageHeader title="Lookup tester" desc="Ask the connected node to pick an instance exactly as clients and DNS would, and see why it chose that one." />
      <div className="grid gap-6 xl:grid-cols-[minmax(0,420px)_1fr]">
        <form
          className="panel h-fit space-y-4 p-5"
          onSubmit={(e) => {
            e.preventDefault();
            void run();
          }}
        >
          <Field label="Service name">
            <input className="field font-mono" list="service-names" value={name} onChange={(e) => setName(e.target.value)} placeholder="payments" required />
            <datalist id="service-names">
              {names.map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
          </Field>
          <div className="grid grid-cols-2 gap-4">
            <Field label="Region" hint="preferred, falls back">
              <select className="field" value={region} onChange={(e) => setRegion(e.target.value)}>
                <option value="">Any</option>
                {regions.map((r) => (
                  <option key={r}>{r}</option>
                ))}
              </select>
            </Field>
            <Field label="Key" hint="same key → same instance">
              <input className="field font-mono" value={key} onChange={(e) => setKey(e.target.value)} placeholder="user-42" />
            </Field>
          </div>
          <Field label="Required tags" hint="comma-separated; instances need all of them">
            <input className="field font-mono" value={tags} onChange={(e) => setTags(e.target.value)} placeholder="primary" />
          </Field>
          <details className="rounded-xl border border-line p-3 text-sm">
            <summary className="cursor-pointer text-muted">Routing policy overrides</summary>
            <div className="mt-3 grid gap-4">
              <Field label="Stale records" hint="instances whose owner node isn't alive">
                <select className="field" value={stale} onChange={(e) => setStale(e.target.value as LookupOptions["stale"])}>
                  <option value="">Node default</option>
                  <option value="prefer-live">prefer-live</option>
                  <option value="include">include</option>
                  <option value="exclude">exclude</option>
                </select>
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label="Latency">
                  <select className="field" value={latency} onChange={(e) => setLatency(e.target.value as LookupOptions["latency"])}>
                    <option value="">Node default</option>
                    <option value="nearest">nearest</option>
                    <option value="off">off</option>
                  </select>
                </Field>
                <Field label="Tolerance" hint="e.g. 10ms">
                  <input className="field font-mono" value={tolerance} onChange={(e) => setTolerance(e.target.value)} disabled={latency !== "nearest"} placeholder="10ms" />
                </Field>
              </div>
            </div>
          </details>
          <button className="btn btn-lime w-full" disabled={busy || !name.trim()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Search className="h-4 w-4" />} Run lookup
          </button>
        </form>

        <div className="space-y-6">
          {!result ? (
            <div className="panel">
              <Empty title="Run a lookup to see the result" desc="Rules, in order: skip draining and unhealthy instances, apply the stale-record policy, prefer healthy, prefer the region, rank by latency, then hash the key or round-robin." />
            </div>
          ) : result.pick ? (
            <section className="panel border-lime/30 p-5" aria-live="polite">
              <p className="text-xs uppercase tracking-wider text-lime">Selected instance</p>
              <p className="mt-2 font-mono text-2xl font-semibold break-all">{result.pick.address}</p>
              <div className="mt-3 flex flex-wrap items-center gap-2 text-sm">
                <StatusBadge tone={healthTone[result.pick.health]}>{result.pick.health}</StatusBadge>
                <span className="font-mono text-muted">{result.pick.id}</span>
                <span className="text-muted">· region {result.pick.region || "—"} · owner {result.pick.node_id}</span>
                {result.pick.version && <span className="text-muted">· v{result.pick.version}</span>}
              </div>
              {result.opts.key ? (
                <p className="mt-3 text-sm text-muted">The same key keeps choosing this instance while the candidate set stays the same.</p>
              ) : (
                <p className="mt-3 text-sm text-muted">No key, so repeated lookups rotate through the equally good candidates.</p>
              )}
            </section>
          ) : (
            <Notice tone={result.code === "no_healthy_instance" ? "warn" : "error"} title="No instance selected">
              {result.error}
            </Notice>
          )}

          {result && (
            <section className="panel p-5">
              <h2 className="font-medium">Candidates for {result.name}</h2>
              <p className="mt-1 text-xs text-muted">Computed in the console from this node&apos;s registry and member view; the node made the actual choice above.</p>
              {verdicts.length === 0 ? (
                <p className="mt-4 text-sm text-muted">No instances of this service are registered.</p>
              ) : (
                <ul className="mt-4 divide-y divide-line">
                  {verdicts.map((v) => (
                    <li key={v.service.id} className={cn("flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-sm", !v.eligible && "opacity-60")}>
                      <StatusBadge tone={v.service.id === result.pick?.id ? "ok" : v.eligible ? "info" : "muted"}>
                        {v.service.id === result.pick?.id ? "selected" : v.eligible ? "candidate" : "skipped"}
                      </StatusBadge>
                      <span className="font-mono">{v.service.address}</span>
                      <span className="text-muted">{v.service.region}</span>
                      {(v.service.tags ?? []).slice(0, 3).map((t) => (
                        <Tag key={t}>{t}</Tag>
                      ))}
                      <span className="w-full text-xs text-muted sm:ml-auto sm:w-auto">{v.reason}</span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {result && <CodeBlock title="the same lookup from a terminal" code={`${curl}\n\n# or through DNS, if the node serves it\ndig @<node> -p 5353 ${result.name}${result.opts.region ? `.${result.opts.region}` : ""}.atlas`} />}
        </div>
      </div>
    </>
  );
}
