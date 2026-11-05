"use client";

import { useState } from "react";
import { CodeBlock } from "@/components/copy-button";
import { cn } from "@/lib/format";

const tabs = [
  {
    id: "docker",
    label: "Docker",
    note: "One container, no setup. Dev mode generates a gossip key and token, enables DNS and allows the console to connect.",
    code: `docker run --rm -p 8080:8080 -p 5353:5353/udp \\
  -v atlas-data:/data ghcr.io/manziosee/atlas:edge`,
  },
  {
    id: "cluster",
    label: "3-node cluster",
    note: "A real cluster with Prometheus and Grafana. The script writes fresh secrets to .env.",
    code: `git clone https://github.com/manziosee/atlas && cd atlas
./scripts/gen-secrets.sh
docker compose up --build -d`,
  },
  {
    id: "agent",
    label: "Docker labels",
    note: "atlas-agent registers labelled containers, keeps them healthy, drains on stop and removes them on exit.",
    code: `docker run -d --label atlas.name=payments \\
  --label atlas.health=/healthz payments:1.4.2`,
  },
  {
    id: "helm",
    label: "Kubernetes",
    note: "Helm installs a 3-node cluster with generated secrets; k8sSync registers every Service you annotate.",
    code: `helm install atlas oci://ghcr.io/manziosee/charts/atlas \
  -n atlas --create-namespace --set k8sSync.enabled=true

kubectl annotate service payments atlas.io/register=true`,
  },
  {
    id: "terraform",
    label: "Terraform",
    note: "Register what can't register itself, and read services and lookups as data.",
    code: `resource "atlas_service" "orders_db" {
  name         = "orders-db"
  address      = "10.0.4.20:5432"
  health_check = "tcp://10.0.4.20:5432"
}`,
  },
  {
    id: "go",
    label: "Go client",
    note: "Clients exist for Go, Python, Node.js/TypeScript and Java. Each has zero or minimal dependencies.",
    code: `client, _ := atlas.NewClient("http://127.0.0.1:8080", token, nil)
svc, _ := client.Register(ctx, atlas.RegisterRequest{
    Name: "payments", Address: "10.0.0.8:8080", TTLSeconds: 15,
})
pick, _ := client.Lookup(ctx, "payments", "eu", "user-42")`,
  },
  {
    id: "connect",
    label: "Connect the console",
    note: "Let this console's origin call your node, then connect with the node URL and a token. Read scope is enough to look around.",
    code: `# on the node (dev mode already allows any origin)
ATLAS_CORS_ORIGINS=https://your-console.netlify.app

# optional: send people from the node to the console
ATLAS_CONSOLE_URL=https://your-console.netlify.app`,
  },
];

export function QuickStart() {
  const [active, setActive] = useState(tabs[0].id);
  const tab = tabs.find((t) => t.id === active) ?? tabs[0];
  return (
    <div>
      <div role="tablist" aria-label="Quick start" className="flex flex-wrap gap-2">
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            id={`qs-tab-${t.id}`}
            aria-selected={t.id === active}
            aria-controls="qs-panel"
            onClick={() => setActive(t.id)}
            className={cn(
              "rounded-full border px-3.5 py-1.5 text-sm transition-colors",
              t.id === active ? "border-lime/50 bg-lime/10 text-lime" : "border-line-strong text-muted hover:text-fg",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div id="qs-panel" role="tabpanel" aria-labelledby={`qs-tab-${tab.id}`} className="mt-5">
        <p className="mb-3 text-sm text-muted">{tab.note}</p>
        <CodeBlock code={tab.code} title={tab.label} />
      </div>
    </div>
  );
}
