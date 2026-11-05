"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowRight, Eye, Loader2 } from "lucide-react";
import { AtlasClient, explain, type Member, type Service } from "@/lib/api";
import { DEMO_TOKEN, DEMO_URL } from "@/lib/demo";
import { useConnection } from "@/lib/connection";

type Snapshot = { members: Member[]; services: Service[]; version: string };

/** Live numbers from the public demo cluster, read with its read-only token. */
export function DemoPreview() {
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [opening, setOpening] = useState(false);
  const { connectDemo } = useConnection();
  const router = useRouter();

  useEffect(() => {
    const client = new AtlasClient(DEMO_URL, DEMO_TOKEN);
    let stop = false;
    const load = () =>
      Promise.all([client.members(), client.services(), client.node()])
        .then(([members, services, info]) => !stop && (setSnap({ members, services, version: info.version }), setError(null)))
        .catch((err) => !stop && setError(explain(err)));
    void load();
    const t = setInterval(load, 20_000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, []);

  const alive = snap?.members.filter((m) => m.state === "alive").length ?? 0;
  const healthy = snap?.services.filter((s) => s.health === "healthy").length ?? 0;
  const names = snap ? new Set(snap.services.map((s) => s.name)).size : 0;
  const stats = [
    { label: "Nodes alive", value: snap ? `${alive}/${snap.members.length}` : "—" },
    { label: "Service instances", value: snap ? String(snap.services.length) : "—" },
    { label: "Healthy", value: snap ? String(healthy) : "—" },
    { label: "Services", value: snap ? String(names) : "—" },
  ];

  const open = async () => {
    setOpening(true);
    try {
      await connectDemo();
      router.push("/dashboard/");
    } catch (err) {
      setError(explain(err));
      setOpening(false);
    }
  };

  return (
    <div className="panel relative overflow-hidden p-6 sm:p-8">
      <div className="pointer-events-none absolute -right-24 -top-24 h-72 w-72 rounded-full bg-teal/20 blur-3xl" />
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="inline-flex items-center gap-2 rounded-full border border-info/40 bg-info/10 px-3 py-1 text-xs text-info">
          <Eye className="h-3.5 w-3.5" /> Read-only demo cluster
        </span>
        <span className="flex items-center gap-2 font-mono text-xs text-muted" role="status">
          {error ? (
            <span className="text-warn">demo unreachable</span>
          ) : snap ? (
            <>
              <span className="animate-pulse-dot h-2 w-2 rounded-full bg-ok" /> live · {snap.version}
            </>
          ) : (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" /> connecting…
            </>
          )}
        </span>
      </div>
      <dl className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        {stats.map((s) => (
          <div key={s.label} className="rounded-xl border border-line bg-bg-2/70 p-4">
            <dt className="text-xs text-muted">{s.label}</dt>
            <dd className="mt-1 font-mono text-2xl font-semibold tabular-nums">{s.value}</dd>
          </div>
        ))}
      </dl>
      {snap && snap.services.length > 0 && (
        <ul className="mt-4 flex flex-wrap gap-2" aria-label="Services in the demo">
          {[...new Set(snap.services.map((s) => s.name))].slice(0, 8).map((n) => (
            <li key={n} className="rounded-full border border-line px-2.5 py-1 font-mono text-xs text-muted">
              {n}
            </li>
          ))}
        </ul>
      )}
      {error && <p className="mt-4 text-sm text-warn">{error}</p>}
      <div className="mt-6 flex flex-wrap gap-3">
        <button type="button" onClick={open} disabled={opening} className="btn btn-lime">
          {opening ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eye className="h-4 w-4" />} Explore the live demo
        </button>
        <Link href="/connect/" className="btn btn-outline">
          Connect your own node <ArrowRight className="h-4 w-4" />
        </Link>
      </div>
      <p className="mt-4 text-xs text-faint">
        The demo uses its own public, read-only token. Never paste a production token into a demo or a page you don't trust.
      </p>
    </div>
  );
}
