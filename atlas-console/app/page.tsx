import Link from "next/link";
import {
  Activity,
  ArrowRight,
  BookOpen,
  Boxes,
  Container,
  FileCode2,
  GitBranch,
  Globe2,
  HeartPulse,
  History,
  KeyRound,
  Layers,
  LifeBuoy,
  Network,
  Radio,
  Route,
  Server,
  Ship,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { SiteNav } from "@/components/landing/site-nav";
import { Planet } from "@/components/landing/planet";
import { HowItWorks } from "@/components/landing/how-it-works";
import { DemoPreview } from "@/components/landing/demo-preview";
import { QuickStart } from "@/components/landing/quick-start";
import { Logo } from "@/components/logo";
import { SiteFooter } from "@/components/site-footer";
import { REPO_URL } from "@/lib/demo";

const builtOn = [
  { icon: Waypoints, label: "SWIM gossip" },
  { icon: Layers, label: "SQLite replicas" },
  { icon: Network, label: "gRPC + HTTP" },
  { icon: Globe2, label: "DNS (A/SRV)" },
  { icon: Activity, label: "Prometheus" },
  { icon: Container, label: "Docker & K8s" },
];

const why = [
  {
    icon: Server,
    title: "No central registry to lose",
    text: "Every node keeps a full, replicated copy of the registry. Lose a node and the rest keep answering; there is no leader to fail over.",
  },
  {
    icon: HeartPulse,
    title: "Only ready instances get traffic",
    text: "HTTP, TCP, gRPC and TTL health checks plus draining mean lookups, DNS and client load balancers skip anything that isn't ready.",
  },
  {
    icon: Radio,
    title: "See the whole cluster, live",
    text: "Membership and service changes stream to the console as they happen, with history to answer “what changed?” after the fact.",
  },
];

const features = [
  { icon: Waypoints, title: "Cluster membership", text: "SWIM failure detection with indirect probes, suspicion and refutation, Lifeguard tuning and encrypted UDP gossip." },
  { icon: HeartPulse, title: "Service health", text: "HTTP, TCP and gRPC-health probes, per-service intervals and thresholds, TTL heartbeats, and draining for safe deploys." },
  { icon: Route, title: "Routing and lookup", text: "Healthy first, region affinity, nearest node by measured latency, consistent hashing by key, and a stale-record policy." },
  { icon: History, title: "Change history", text: "Every registration, update, health change and removal is recorded per service and across the cluster." },
  { icon: Radio, title: "Event streaming", text: "Server-Sent Events and gRPC Watch with Last-Event-ID resume, so consumers never silently miss a change." },
  { icon: Globe2, title: "DNS discovery", text: "A, AAAA and SRV records for healthy instances, per region, with short TTLs for unmodified applications." },
  { icon: Container, title: "Docker-label agent", text: "Add atlas.name to a container and it's registered, health-checked, drained on stop and removed on exit." },
  { icon: Ship, title: "Kubernetes controller & Helm", text: "Install with one Helm command; annotate a Service and every ready pod is registered, terminating pods drained." },
  { icon: FileCode2, title: "Terraform provider", text: "Register databases and appliances as code, and read services and lookups as Terraform data." },
  { icon: Globe2, title: "Cluster map", text: "A live graph, world map and matrix of the latency every node measures to every other." },
  { icon: Boxes, title: "Clients for every stack", text: "Go (with an http.RoundTripper and gRPC resolver), Python, Node.js/TypeScript and Java clients." },
  { icon: ShieldCheck, title: "Secure by default", text: "TLS 1.3 and mTLS, scoped expiring tokens, rate limits, an admission allowlist and fail-closed configuration." },
];

const docs = [
  { icon: BookOpen, title: "Read the docs", text: "Quick start, console, configuration and deployment for Compose, Kubernetes and Fly.io.", href: "/docs/" },
  { icon: FileCode2, title: "API reference", text: "Every HTTP endpoint, parameter, response and error code.", href: "/api/" },
  { icon: Ship, title: "Kubernetes & Helm", text: "Install with Helm and register annotated Services automatically.", href: "/docs/kubernetes/" },
  { icon: FileCode2, title: "Terraform provider", text: "Manage registrations as code and read services as data.", href: "/docs/terraform/" },
  { icon: LifeBuoy, title: "Runbooks", text: "What to do when an alert fires or a node misbehaves.", href: "/docs/runbooks/" },
  { icon: GitBranch, title: "Source on GitHub", text: "Apache-2.0. Issues, discussions and contributions welcome.", href: REPO_URL },
];

const faqs = [
  {
    q: "Why is there no sign-up or login?",
    a: "Atlas authenticates every API request with a bearer token scoped to read, write or admin. There are no user accounts. The console asks for a node URL and a token, and a read token is enough to view everything.",
  },
  {
    q: "Where does my token go?",
    a: "Only to the node you connect to. The console is a static site that runs in your browser and calls your node directly; nothing passes through a console server. The token stays in page memory unless you choose to keep it for this tab.",
  },
  {
    q: "The console can't reach my node. What now?",
    a: "Browsers only let a page call another origin if that origin allows it. Set ATLAS_CORS_ORIGINS on the node to this site's origin (dev mode allows any origin), and make sure the node URL is reachable from your browser.",
  },
  {
    q: "Is there a single point of failure?",
    a: "No. Any node accepts any request. Writes are forwarded to the node that owns a service, and reads are answered from each node's local replica.",
  },
];

function Section({ id, eyebrow, title, sub, children }: { id: string; eyebrow: string; title: string; sub?: string; children: React.ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="mx-auto max-w-6xl px-4 py-20 sm:px-6 sm:py-28">
      <div className="mx-auto max-w-2xl text-center">
        <span className="eyebrow">{eyebrow}</span>
        <h2 id={`${id}-title`} className="text-balance mt-4 text-3xl font-medium tracking-tight sm:text-[2.6rem] sm:leading-[1.1]">
          {title}
        </h2>
        {sub && <p className="mt-4 text-muted">{sub}</p>}
      </div>
      <div className="mt-12 sm:mt-14">{children}</div>
    </section>
  );
}

export default function Landing() {
  return (
    <>
      <SiteNav />
      <main id="main">
        {/* Hero */}
        <section className="relative overflow-hidden">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(60%_50%_at_85%_10%,rgb(66_168_245/0.18),transparent_70%),radial-gradient(50%_40%_at_10%_20%,rgb(47_211_181/0.12),transparent_70%)]" />
          <div className="relative mx-auto max-w-4xl px-4 pt-36 text-center sm:px-6 sm:pt-44">
            <a href="#quick-start" className="inline-flex items-center gap-2 rounded-full border border-line-strong bg-white/[0.03] py-1 pl-1.5 pr-3 text-xs text-muted transition-colors hover:text-fg">
              <span className="grid h-5 w-5 place-items-center rounded-full bg-lime/15">
                <Logo className="h-3.5 w-3.5" />
              </span>
              <span>Dev mode &amp; Docker-label agent</span>
              <span className="text-line-strong">•</span>
              <span className="text-fg">Try it in one command</span>
              <ArrowRight className="h-3.5 w-3.5" />
            </a>
            <h1 className="text-balance mt-7 text-[2.5rem] font-medium leading-[1.05] tracking-tight sm:text-6xl lg:text-[4.2rem]">
              Know which services are ready, across every node.
            </h1>
            <p className="text-balance mx-auto mt-6 max-w-2xl text-[1.05rem] leading-relaxed text-muted">
              Atlas is a distributed service-discovery system. Every node keeps a local, replicated view of your cluster in sync, so
              lookups stay fast and correct without relying on a central registry.
            </p>
            <div className="mt-9 flex flex-wrap items-center justify-center gap-3">
              <a href="#demo" className="btn btn-outline">
                Explore the live demo
              </a>
              <a href="#quick-start" className="btn btn-lime">
                Get started <ArrowRight className="h-4 w-4" />
              </a>
            </div>
          </div>
          <Planet />
          <div className="relative mx-auto -mt-16 max-w-5xl px-4 pb-16 text-center sm:-mt-24">
            <p className="text-xs text-muted">Built on proven, boring technology</p>
            <ul className="mt-5 flex flex-wrap items-center justify-center gap-x-8 gap-y-4">
              {builtOn.map(({ icon: Icon, label }) => (
                <li key={label} className="flex items-center gap-2 text-[0.95rem] font-medium text-fg/80">
                  <Icon className="h-4.5 w-4.5 text-fg/60" /> {label}
                </li>
              ))}
            </ul>
          </div>
        </section>

        {/* Why */}
        <div className="border-t border-line bg-bg-2/40">
          <Section id="why" eyebrow="Why Atlas" title="Never route a request to a service that isn't ready" sub="Three reasons platform teams pick Atlas over a central registry.">
            <div className="grid overflow-hidden rounded-2xl border border-line md:grid-cols-3">
              {why.map(({ icon: Icon, title, text }, i) => (
                <article key={title} className={`bg-panel/60 p-7 ${i > 0 ? "border-t border-line md:border-l md:border-t-0" : ""}`}>
                  <div className="icon-tile h-14 w-14">
                    <Icon className="h-6 w-6 text-fg" />
                  </div>
                  <h3 className="mt-10 font-medium">{title}</h3>
                  <p className="mt-2 text-sm leading-relaxed text-muted">{text}</p>
                </article>
              ))}
            </div>
          </Section>
        </div>

        {/* How it works */}
        <Section id="how" eyebrow="How it works" title="From one node to a global cluster in four steps" sub="Nodes gossip membership and service records; every node can answer every lookup.">
          <HowItWorks />
        </Section>

        {/* Live demo */}
        <div className="border-y border-line bg-bg-2/40">
          <Section id="demo" eyebrow="Live demo" title="Explore a real cluster, read-only" sub="A small Atlas cluster runs on Fly.io. Open it in the console to browse nodes, services, events and lookups.">
            <div className="mx-auto max-w-4xl">
              <DemoPreview />
            </div>
          </Section>
        </div>

        {/* Features */}
        <Section id="features" eyebrow="Features" title="Everything service discovery needs, nothing it doesn't">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {features.map(({ icon: Icon, title, text }) => (
              <article key={title} className="panel p-6 transition-colors hover:border-line-strong">
                <div className="icon-tile h-11 w-11">
                  <Icon className="h-5 w-5" />
                </div>
                <h3 className="mt-5 font-medium">{title}</h3>
                <p className="mt-2 text-sm leading-relaxed text-muted">{text}</p>
              </article>
            ))}
          </div>
        </Section>

        {/* Console */}
        <div className="border-y border-line bg-bg-2/40">
          <Section id="console" eyebrow="The console" title="Operate the cluster from your browser" sub="Connect with a node URL and a token. What you can do follows the token's scope.">
            <div className="grid gap-4 md:grid-cols-3">
              {[
                { scope: "read", title: "Observe", items: ["Cluster overview and node states", "Searchable services and health", "Live activity and change history", "Lookup tester with routing details"] },
                { scope: "write", title: "Operate", items: ["Register and edit services", "Drain and undrain instances", "Send TTL heartbeats", "Deregister with confirmation"] },
                { scope: "admin", title: "Administer", items: ["List credentials (no secrets)", "Download a registry backup", "Evict failed nodes", "Everything read and write can do"] },
              ].map((c) => (
                <div key={c.scope} className="panel p-6">
                  <span className="rounded-full border border-line-strong px-2 py-0.5 font-mono text-xs text-lime">{c.scope}</span>
                  <h3 className="mt-4 text-lg font-medium">{c.title}</h3>
                  <ul className="mt-3 space-y-2 text-sm text-muted">
                    {c.items.map((i) => (
                      <li key={i} className="flex gap-2">
                        <span className="mt-2 h-1 w-1 shrink-0 rounded-full bg-teal" /> {i}
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </div>
            <div className="mt-8 text-center">
              <Link href="/connect/" className="btn btn-lime">
                Open the console <ArrowRight className="h-4 w-4" />
              </Link>
            </div>
          </Section>
        </div>

        {/* Quick start */}
        <Section id="quick-start" eyebrow="Quick start" title="Running in under a minute">
          <div className="mx-auto max-w-3xl">
            <QuickStart />
            <div className="panel mt-8 flex flex-wrap items-center justify-between gap-4 p-5">
              <div>
                <p className="font-medium">Connect your node</p>
                <p className="text-sm text-muted">Paste the node URL and the token it printed. A read-only token is enough to look around.</p>
              </div>
              <Link href="/connect/" className="btn btn-lime">
                <KeyRound className="h-4 w-4" /> Connect
              </Link>
            </div>
          </div>
        </Section>

        {/* Docs */}
        <div className="border-t border-line bg-bg-2/40">
          <Section id="docs" eyebrow="Docs & API" title="Learn it, script it, run it">
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {docs.map(({ icon: Icon, title, text, href }) => (
                <a key={title} href={href} {...(href.startsWith("http") ? { target: "_blank", rel: "noreferrer" } : {})} className="panel group p-6 transition-colors hover:border-line-strong">
                  <Icon className="h-5 w-5 text-teal" />
                  <h3 className="mt-4 flex items-center gap-1.5 font-medium">
                    {title} <ArrowRight className="h-4 w-4 opacity-0 transition-opacity group-hover:opacity-100" />
                  </h3>
                  <p className="mt-1.5 text-sm text-muted">{text}</p>
                </a>
              ))}
            </div>
          </Section>
        </div>

        {/* Security */}
        <Section id="security" eyebrow="Security" title="Tokens, scopes and what the console never does">
          <div className="mx-auto grid max-w-5xl gap-4 md:grid-cols-2">
            <div className="panel p-6">
              <ShieldCheck className="h-5 w-5 text-lime" />
              <h3 className="mt-4 font-medium">Least privilege by design</h3>
              <ul className="mt-3 space-y-2.5 text-sm text-muted">
                <li><span className="font-mono text-fg">read</span> — view members, services, history and events.</li>
                <li><span className="font-mono text-fg">write</span> — register, update, drain, heartbeat and deregister.</li>
                <li><span className="font-mono text-fg">admin</span> — credentials, backups and evicting failed nodes.</li>
              </ul>
              <p className="mt-4 text-sm text-muted">Tokens can expire. Rotate them with a config reload (SIGHUP), with no restart.</p>
            </div>
            <div className="panel p-6">
              <KeyRound className="h-5 w-5 text-warn" />
              <h3 className="mt-4 font-medium">Keep production tokens private</h3>
              <ul className="mt-3 space-y-2.5 text-sm text-muted">
                <li>The demo has its own public, read-only token. Never paste a production token into a public demo.</li>
                <li>The console talks to your node directly from your browser; it has no backend that could store tokens.</li>
                <li>Tokens live in page memory and are forgotten when you disconnect or close the tab.</li>
                <li>Credential creation and revocation happen in node config, not in the browser.</li>
              </ul>
            </div>
          </div>
        </Section>

        {/* FAQ */}
        <div className="border-t border-line bg-bg-2/40">
          <Section id="faq" eyebrow="FAQ" title="Good questions">
            <div className="mx-auto max-w-3xl divide-y divide-line rounded-2xl border border-line bg-panel/60">
              {faqs.map((f) => (
                <details key={f.q} className="group p-5 [&_summary::-webkit-details-marker]:hidden">
                  <summary className="flex cursor-pointer list-none items-center justify-between gap-4 font-medium">
                    {f.q}
                    <span className="text-xl leading-none text-muted transition-transform group-open:rotate-45" aria-hidden>
                      +
                    </span>
                  </summary>
                  <p className="mt-3 text-sm leading-relaxed text-muted">{f.a}</p>
                </details>
              ))}
            </div>
          </Section>
        </div>

        {/* CTA */}
        <section className="relative overflow-hidden border-t border-line">
          <div className="pointer-events-none absolute inset-x-0 bottom-[-60%] mx-auto h-[120%] max-w-4xl rounded-full bg-[radial-gradient(closest-side,rgb(47_211_181/0.25),transparent)]" />
          <div className="relative mx-auto max-w-3xl px-4 py-24 text-center sm:px-6">
            <h2 className="text-balance text-3xl font-medium tracking-tight sm:text-5xl">Give every node the whole picture.</h2>
            <p className="mx-auto mt-4 max-w-xl text-muted">Start a node in one command, then watch your services come alive in the console.</p>
            <div className="mt-8 flex flex-wrap justify-center gap-3">
              <a href="#demo" className="btn btn-outline">
                Explore the live demo
              </a>
              <a href="#quick-start" className="btn btn-lime">
                Get started <ArrowRight className="h-4 w-4" />
              </a>
            </div>
          </div>
        </section>
      </main>

      <SiteFooter />
    </>
  );
}
