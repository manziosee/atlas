import type { Metadata } from "next";
import Link from "next/link";
import { ArrowRight, Globe2, HeartPulse, Server, ShieldCheck } from "lucide-react";
import { ProsePage } from "@/components/prose-page";
import { REPO_URL } from "@/lib/demo";

export const metadata: Metadata = { title: "About", description: "Why Atlas exists, how it's built and who maintains it." };

const principles = [
  { icon: Server, title: "No single point of failure", text: "Every node keeps the whole registry. Lose any node, or a whole region, and the rest keep answering." },
  { icon: HeartPulse, title: "Only ready instances get traffic", text: "Health checks, heartbeats and draining decide what clients, DNS and load balancers see." },
  { icon: ShieldCheck, title: "Secure by default", text: "Encrypted gossip, TLS and scoped tokens. A configuration that's missing a secret refuses to start rather than running open." },
  { icon: Globe2, title: "Built for the edge", text: "Region-aware routing and measured latency, so a request in Paris finds the nearest healthy instance." },
];

export default function AboutPage() {
  return (
    <ProsePage
      eyebrow="About"
      title="Why we built Atlas"
      intro={
        <>
          Services move. Containers restart, pods reschedule and regions fail over. Something has to know, at every moment, which instances are ready and
          where they are. Atlas does that without a central registry that can itself become the outage.
        </>
      }
    >
      <div className="mt-10 grid gap-4 sm:grid-cols-2">
        {principles.map(({ icon: Icon, title, text }) => (
          <div key={title} className="panel p-5">
            <Icon className="h-5 w-5 text-lime" />
            <p className="mt-3 font-medium">{title}</p>
            <p className="mt-1 text-sm text-muted">{text}</p>
          </div>
        ))}
      </div>

      <div className="prose mt-10">
        <h2 id="how-its-built">How it&apos;s built</h2>
        <p>
          Atlas is written in Go. Nodes find each other and detect failures with the SWIM gossip protocol, so there is no leader to elect and nothing to
          bootstrap. Each node stores the service registry in SQLite and reconciles it with its peers. Every record carries a revision, so replicas converge
          on the same answer. Any node accepts any request, and writes reach the node that owns a service.
        </p>
        <p>
          Around that core there is an HTTP and gRPC API, DNS, client libraries for Go, Python, Node.js and Java, a Docker-label agent, a Kubernetes
          controller, a Helm chart, a Terraform provider and the Atlas Console you&apos;re looking at.
        </p>
        <h2 id="open-source">Open source</h2>
        <p>
          Atlas is free software under the Apache License 2.0. The code, the issue tracker, the design decisions and the roadmap are all public. If Atlas
          is useful to you, the best thanks is a bug report, a docs fix or a star on GitHub.
        </p>
        <h2 id="who">Who maintains it</h2>
        <p>
          Atlas is maintained by Manzi Osee, with contributions from the community. Questions, ideas and partnership requests are welcome at{" "}
          <a href="mailto:manziosee@gmail.com">manziosee@gmail.com</a>. Please report security issues privately, as described on the{" "}
          <Link href="/security/">security page</Link>.
        </p>
      </div>

      <div className="mt-10 flex flex-wrap gap-3">
        <Link href="/docs/quick-start/" className="btn btn-lime">
          Get started <ArrowRight className="h-4 w-4" />
        </Link>
        <a href={REPO_URL} target="_blank" rel="noreferrer" className="btn btn-outline">
          Source on GitHub
        </a>
        <Link href="/contributing/" className="btn btn-outline">
          Contribute
        </Link>
      </div>
    </ProsePage>
  );
}
