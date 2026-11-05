"use client";

import Link from "next/link";
import { useMemo } from "react";
import { ArrowRight, Box, Container, FileCode2, Ship } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { useConnection } from "@/lib/connection";
import { sourceOf } from "@/lib/source";
import { CodeBlock } from "@/components/copy-button";
import { PageHeader, StatusBadge } from "@/components/console/ui";

type Owner = { owner: string; instances: number; healthy: number; services: Set<string>; namespaces: Set<string> };

function group(list: ReturnType<typeof useCluster>["services"], kind: "kubernetes" | "docker") {
  const owners = new Map<string, Owner>();
  for (const s of list) {
    const src = sourceOf(s);
    if (src.kind !== kind) continue;
    const key = src.owner || "default";
    const o = owners.get(key) ?? { owner: key, instances: 0, healthy: 0, services: new Set(), namespaces: new Set() };
    o.instances++;
    if (s.health === "healthy") o.healthy++;
    o.services.add(s.name);
    if (src.namespace) o.namespaces.add(src.namespace);
    owners.set(key, o);
  }
  return [...owners.values()].sort((a, b) => a.owner.localeCompare(b.owner));
}

function Detected({ owners, noun, empty }: { owners: Owner[]; noun: string; empty: string }) {
  if (!owners.length) return <p className="text-sm text-muted">{empty}</p>;
  return (
    <ul className="space-y-2">
      {owners.map((o) => (
        <li key={o.owner} className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-line px-3 py-2 text-sm">
          <span>
            <span className="text-muted">{noun}</span> <span className="font-mono">{o.owner}</span>
            {o.namespaces.size > 0 && <span className="text-xs text-muted"> · {o.namespaces.size} namespaces</span>}
          </span>
          <span className="flex items-center gap-2 text-xs">
            <span className="text-muted">
              {o.services.size} services · {o.instances} instances
            </span>
            <StatusBadge tone={o.healthy === o.instances ? "ok" : "warn"}>{o.healthy} healthy</StatusBadge>
          </span>
        </li>
      ))}
    </ul>
  );
}

export default function IntegrationsPage() {
  const { services } = useCluster();
  const { conn } = useConnection();
  const k8s = useMemo(() => group(services, "kubernetes"), [services]);
  const docker = useMemo(() => group(services, "docker"), [services]);
  const nodeURL = conn?.mode === "demo" ? "https://atlas.example.com:8080" : conn?.url;

  return (
    <>
      <PageHeader title="Integrations" desc="Ways to get services into Atlas without changing them, and to manage Atlas as code. Detected sources come from the owner tags each integration adds." />
      <div className="grid gap-6 xl:grid-cols-2">
        <section className="panel p-6" aria-labelledby="k8s">
          <div className="flex items-center gap-3">
            <div className="icon-tile h-10 w-10">
              <Ship className="h-5 w-5" />
            </div>
            <div>
              <h2 id="k8s" className="font-medium">
                Kubernetes
              </h2>
              <p className="text-xs text-muted">atlas-k8s registers annotated Services, one instance per ready pod.</p>
            </div>
          </div>
          <div className="mt-5">
            <Detected owners={k8s} noun="Cluster" empty="No Kubernetes cluster is registering services here yet." />
          </div>
          <div className="mt-5 space-y-3">
            <CodeBlock title="install with Helm" code={`helm install atlas oci://ghcr.io/manziosee/charts/atlas \\\n  -n atlas --create-namespace --set k8sSync.enabled=true`} />
            <CodeBlock title="opt a Service in" code={`kubectl annotate service payments \\\n  atlas.io/register=true atlas.io/health=/healthz`} />
          </div>
          <Link href="/docs/kubernetes/" className="mt-4 inline-flex items-center gap-1 text-sm text-teal hover:underline">
            Kubernetes guide <ArrowRight className="h-4 w-4" />
          </Link>
        </section>

        <section className="panel p-6" aria-labelledby="docker">
          <div className="flex items-center gap-3">
            <div className="icon-tile h-10 w-10">
              <Container className="h-5 w-5" />
            </div>
            <div>
              <h2 id="docker" className="font-medium">
                Docker
              </h2>
              <p className="text-xs text-muted">atlas-agent registers containers labelled atlas.name.</p>
            </div>
          </div>
          <div className="mt-5">
            <Detected owners={docker} noun="Host" empty="No Docker host is registering containers here yet." />
          </div>
          <div className="mt-5 space-y-3">
            <CodeBlock title="label a container" code={`docker run -d --label atlas.name=payments \\\n  --label atlas.health=/healthz payments:1.4.2`} />
          </div>
          <Link href="/docs/docker-agent/" className="mt-4 inline-flex items-center gap-1 text-sm text-teal hover:underline">
            Docker agent guide <ArrowRight className="h-4 w-4" />
          </Link>
        </section>

        <section className="panel p-6" aria-labelledby="tf">
          <div className="flex items-center gap-3">
            <div className="icon-tile h-10 w-10">
              <FileCode2 className="h-5 w-5" />
            </div>
            <div>
              <h2 id="tf" className="font-medium">
                Terraform
              </h2>
              <p className="text-xs text-muted">Register what can&apos;t register itself; read services and lookups as data.</p>
            </div>
          </div>
          <div className="mt-5">
            <CodeBlock
              title="main.tf"
              code={`provider "atlas" {\n  address = "${nodeURL}"   # token from ATLAS_TOKEN\n}\n\nresource "atlas_service" "orders_db" {\n  name         = "orders-db"\n  address      = "10.0.4.20:5432"\n  health_check = "tcp://10.0.4.20:5432"\n}\n\ndata "atlas_services" "payments" {\n  name   = "payments"\n  health = "healthy"\n}`}
            />
          </div>
          <Link href="/docs/terraform/" className="mt-4 inline-flex items-center gap-1 text-sm text-teal hover:underline">
            Terraform provider guide <ArrowRight className="h-4 w-4" />
          </Link>
        </section>

        <section className="panel p-6" aria-labelledby="clients">
          <div className="flex items-center gap-3">
            <div className="icon-tile h-10 w-10">
              <Box className="h-5 w-5" />
            </div>
            <div>
              <h2 id="clients" className="font-medium">
                Client libraries and DNS
              </h2>
              <p className="text-xs text-muted">For services that register themselves and look others up.</p>
            </div>
          </div>
          <ul className="mt-5 space-y-2 text-sm text-muted">
            <li>
              <span className="text-fg">Go</span>: client, load-balancing <span className="font-mono">http.RoundTripper</span> and gRPC resolver
            </li>
            <li>
              <span className="text-fg">Python</span>: sync and asyncio, standard library only
            </li>
            <li>
              <span className="text-fg">Node.js / TypeScript</span> and <span className="text-fg">Java</span>: no runtime dependencies
            </li>
            <li>
              <span className="text-fg">DNS</span>: <span className="font-mono">payments.atlas</span> A/AAAA and SRV records for healthy instances
            </li>
          </ul>
          <Link href="/docs/" className="mt-4 inline-flex items-center gap-1 text-sm text-teal hover:underline">
            All documentation <ArrowRight className="h-4 w-4" />
          </Link>
        </section>
      </div>
    </>
  );
}
