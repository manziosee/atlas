"use client";

import { useState } from "react";
import { Cable, Radar, Rocket, Search } from "lucide-react";
import { Logo } from "@/components/logo";
import { CopyButton } from "@/components/copy-button";
import { cn } from "@/lib/format";

const steps = [
  {
    icon: Rocket,
    title: "Run a node",
    text: "Start Atlas anywhere: one container, a VM or a pod. With no settings it starts in dev mode and prints a token.",
    file: "terminal",
    code: `docker run -p 8080:8080 -p 5353:5353/udp \\
  ghcr.io/manziosee/atlas:edge

# Atlas is running in dev mode...
#   API token  xR4w...`,
  },
  {
    icon: Cable,
    title: "Join peers",
    text: "Point new nodes at any existing one. SWIM gossip spreads membership, detects failures and repairs gaps.",
    file: ".env",
    code: `ATLAS_ENV=production
ATLAS_GOSSIP_KEY=<atlas-cli keygen>
ATLAS_API_TOKENS=<atlas-cli tokengen>
ATLAS_SEEDS=atlas-1:7946,atlas-2:7946`,
  },
  {
    icon: Radar,
    title: "Register services",
    text: "Register over HTTP, gRPC or a client library, or just label a container. Atlas checks health for you.",
    file: "register.sh",
    code: `curl -X POST $ATLAS/v1/services \\
  -H "Authorization: Bearer $TOKEN" \\
  -d '{"name":"payments","address":"10.0.0.8:8080",
       "health_check":"http://10.0.0.8:8080/healthz"}'

# or: docker run --label atlas.name=payments ...`,
  },
  {
    icon: Search,
    title: "Discover from any node",
    text: "Every node answers lookups from its own replica: healthy first, nearest region, stable hashing by key.",
    file: "lookup.sh",
    code: `curl "$ATLAS/v1/lookup/payments?region=eu&key=user-42" \\
  -H "Authorization: Bearer $TOKEN"

dig @127.0.0.1 -p 5353 payments.atlas`,
  },
];

export function HowItWorks() {
  const [active, setActive] = useState(0);
  const step = steps[active];
  return (
    <div className="grid items-stretch gap-8 lg:grid-cols-[1fr_1.15fr]">
      <ol className="flex flex-col" aria-label="Steps">
        {steps.map((s, i) => {
          const Icon = s.icon;
          const on = i === active;
          return (
            <li key={s.title}>
              <button
                type="button"
                onClick={() => setActive(i)}
                aria-current={on ? "step" : undefined}
                className={cn(
                  "group flex w-full gap-4 border-l-2 px-5 py-5 text-left transition-colors",
                  on ? "border-lime bg-gradient-to-r from-lime/[0.06] to-transparent" : "border-line hover:border-line-strong",
                )}
              >
                <Icon className={cn("mt-0.5 h-5 w-5 shrink-0", on ? "text-lime" : "text-muted group-hover:text-fg")} />
                <span>
                  <span className={cn("block font-medium", on ? "text-fg" : "text-fg/85")}>
                    {i + 1} — {s.title}
                  </span>
                  <span className="mt-1 block text-sm leading-relaxed text-muted">{s.text}</span>
                </span>
              </button>
            </li>
          );
        })}
      </ol>

      <div className="starfield relative overflow-hidden rounded-2xl border border-line bg-[radial-gradient(80%_60%_at_80%_110%,rgb(182_242_122/0.25),transparent_60%),radial-gradient(60%_50%_at_0%_0%,rgb(66_168_245/0.12),transparent_70%)] bg-bg-2 p-6 sm:p-8">
        <div className="grid h-11 w-11 place-items-center rounded-xl border border-line-strong bg-panel-2">
          <Logo className="h-6 w-6" />
        </div>
        <div className="ml-5 h-6 w-px bg-gradient-to-b from-line-strong to-transparent" />
        <div className="panel ml-auto max-w-lg overflow-hidden shadow-2xl shadow-black/50 lg:-mt-2">
          <div className="flex items-center gap-1.5 border-b border-line px-3 py-2.5">
            <span className="h-2.5 w-2.5 rounded-full bg-white/15" />
            <span className="h-2.5 w-2.5 rounded-full bg-white/15" />
            <span className="h-2.5 w-2.5 rounded-full bg-white/15" />
            <span className="ml-2 font-mono text-xs text-faint">{step.file}</span>
            <CopyButton text={step.code} className="ml-auto" />
          </div>
          <pre className="min-h-[170px] overflow-x-auto p-4 font-mono text-[0.78rem] leading-relaxed text-fg/90" aria-live="polite">
            <code>{step.code}</code>
          </pre>
        </div>
        <div className="mt-6 flex items-center justify-end gap-3 text-xs text-muted">
          <span className="rounded-lg border border-line-strong bg-panel-2 px-2.5 py-1.5 font-mono">step {active + 1} / {steps.length}</span>
          <span className="flex items-center gap-1.5 rounded-lg border border-lime/30 bg-lime/10 px-2.5 py-1.5 text-lime">
            <span className="animate-pulse-dot h-1.5 w-1.5 rounded-full bg-current" /> no central registry
          </span>
        </div>
      </div>
    </div>
  );
}
