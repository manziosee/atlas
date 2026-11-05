// Build-time documentation loader. Pages are rendered from the repository's
// own Markdown (and README sections), so editing a doc in the repo updates
// the website on the next deploy. Runs only during `next build`/`next dev`.

import fs from "node:fs";
import path from "node:path";
import { Marked, type Tokens } from "marked";

const REPO = "https://github.com/manziosee/atlas";
export const repoRoot = path.resolve(process.cwd(), "..");

type Source =
  | { file: string; shift?: number }
  | { file: string; section: string; shift?: number };

export interface DocPage {
  slug: string;
  title: string;
  description: string;
  group: string;
  sources: Source[];
}

export const docGroups = ["Getting started", "Using Atlas", "Integrations", "Operations", "Internals"] as const;

export const docPages: DocPage[] = [
  { slug: "introduction", group: "Getting started", title: "Why Atlas", description: "What Atlas is and the problems it solves.", sources: [{ file: "README.md", section: "## Why Atlas" }] },
  {
    slug: "quick-start",
    group: "Getting started",
    title: "Quick start",
    description: "One container, labelled containers, or a three-node cluster.",
    sources: [
      { file: "README.md", section: "### One container, no setup", shift: -1 },
      { file: "README.md", section: "### Register containers without code changes", shift: -1 },
      { file: "README.md", section: "### Three-node cluster", shift: -1 },
    ],
  },
  { slug: "console", group: "Getting started", title: "Atlas Console", description: "The web console: screens, scopes, running and deploying it.", sources: [{ file: "README.md", section: "## Atlas Console" }] },
  { slug: "walkthrough", group: "Getting started", title: "Walkthrough", description: "A service from registration to failover.", sources: [{ file: "README.md", section: "## Walkthrough: a service from registration to failover" }] },
  { slug: "registry-api", group: "Using Atlas", title: "Service registry", description: "Register, update, look up and watch services.", sources: [{ file: "README.md", section: "### Service registry API", shift: -1 }] },
  { slug: "dns", group: "Using Atlas", title: "DNS discovery", description: "A, AAAA and SRV records for healthy instances.", sources: [{ file: "README.md", section: "### DNS discovery", shift: -1 }] },
  {
    slug: "clients",
    group: "Using Atlas",
    title: "Client libraries",
    description: "Go, Python, Node.js/TypeScript and Java, plus client-side load balancing.",
    sources: [
      { file: "README.md", section: "### Python client", shift: -1 },
      { file: "README.md", section: "### Node.js / TypeScript client", shift: -1 },
      { file: "README.md", section: "### Java client", shift: -1 },
      { file: "README.md", section: "### Client-side load balancing", shift: -1 },
    ],
  },
  { slug: "configuration", group: "Using Atlas", title: "Configuration", description: "Every ATLAS_* setting and key rotation.", sources: [{ file: "README.md", section: "## Configuration" }] },
  { slug: "kubernetes", group: "Integrations", title: "Kubernetes", description: "Helm install and the atlas-k8s controller.", sources: [{ file: "docs/KUBERNETES.md" }] },
  { slug: "helm", group: "Integrations", title: "Helm chart", description: "Chart values and secrets.", sources: [{ file: "deploy/helm/atlas/README.md" }] },
  { slug: "docker-agent", group: "Integrations", title: "Docker agent", description: "Register containers from labels.", sources: [{ file: "docs/DOCKER_AGENT.md" }] },
  {
    slug: "terraform",
    group: "Integrations",
    title: "Terraform provider",
    description: "Manage services as code; read services, lookups and members.",
    sources: [
      { file: "terraform-provider-atlas/README.md" },
      { file: "terraform-provider-atlas/docs/resources/service.md", shift: 1 },
      { file: "terraform-provider-atlas/docs/data-sources/services.md", shift: 1 },
    ],
  },
  { slug: "deploy", group: "Operations", title: "Deploy", description: "Kubernetes and Fly.io deployments.", sources: [{ file: "README.md", section: "## Deploy" }] },
  { slug: "observability", group: "Operations", title: "Observability", description: "Metrics, alerts and dashboards.", sources: [{ file: "README.md", section: "## Observability" }] },
  {
    slug: "runbooks",
    group: "Operations",
    title: "Runbooks",
    description: "What to do when an alert fires.",
    sources: [
      { file: "docs/runbooks/atlas-api-availability.md", shift: 1 },
      { file: "docs/runbooks/atlas-api-latency.md", shift: 1 },
      { file: "docs/runbooks/atlas-membership.md", shift: 1 },
    ],
  },
  { slug: "architecture", group: "Internals", title: "Architecture", description: "Gossip, replication, ownership and health checks.", sources: [{ file: "docs/ARCHITECTURE.md" }] },
  {
    slug: "decisions",
    group: "Internals",
    title: "Design decisions",
    description: "Architecture decision records.",
    sources: [
      { file: "docs/adr/0001-swim-gossip-over-consensus.md", shift: 1 },
      { file: "docs/adr/0002-gossip-encryption.md", shift: 1 },
      { file: "docs/adr/0003-api-authentication.md", shift: 1 },
      { file: "docs/adr/0004-service-merge.md", shift: 1 },
    ],
  },
  { slug: "roadmap", group: "Internals", title: "Roadmap", description: "What's done and what's next.", sources: [{ file: "README.md", section: "## Roadmap" }] },
];

/** Repository files that have their own page on the site. */
const fileRoutes: Record<string, string> = {
  "docs/ARCHITECTURE.md": "/docs/architecture/",
  "docs/DOCKER_AGENT.md": "/docs/docker-agent/",
  "docs/KUBERNETES.md": "/docs/kubernetes/",
  "deploy/helm/atlas/README.md": "/docs/helm/",
  "terraform-provider-atlas/README.md": "/docs/terraform/",
  "atlas-console/README.md": "/docs/console/",
  "SECURITY.md": "/security/",
  "CONTRIBUTING.md": "/contributing/",
  "api/v1/openapi.yaml": "/api/",
  "LICENSE": "/terms/#license",
};

/** README anchors that became pages. */
const readmeAnchors: Record<string, string> = {
  "why-atlas": "/docs/introduction/",
  "quick-start": "/docs/quick-start/",
  "atlas-console": "/docs/console/",
  "walkthrough-a-service-from-registration-to-failover": "/docs/walkthrough/",
  "service-registry-api": "/docs/registry-api/",
  "dns-discovery": "/docs/dns/",
  "python-client": "/docs/clients/#python-client",
  "nodejs--typescript-client": "/docs/clients/#nodejs--typescript-client",
  "java-client": "/docs/clients/#java-client",
  "client-side-load-balancing": "/docs/clients/#client-side-load-balancing",
  configuration: "/docs/configuration/",
  deploy: "/docs/deploy/",
  observability: "/docs/observability/",
  roadmap: "/docs/roadmap/",
  security: "/security/",
  "live-demo": "/#demo",
  terraform: "/docs/terraform/",
};

export function slugify(text: string) {
  return text
    .toLowerCase()
    .replace(/<[^>]+>/g, "")
    .replace(/&[a-z]+;/g, "")
    .replace(/[^\w\s-]/g, "")
    .trim()
    .replace(/\s/g, "-");
}

function read(file: string): string | null {
  try {
    return fs.readFileSync(path.join(repoRoot, file), "utf8").replace(/\r\n/g, "\n");
  } catch {
    return null;
  }
}

/** The body under a heading, up to the next heading of the same or higher level. */
export function extractSection(md: string, heading: string): string | null {
  const level = heading.match(/^#+/)?.[0].length ?? 2;
  const lines = md.split("\n");
  let start = -1;
  let inFence = false;
  for (let i = 0; i < lines.length; i++) {
    if (/^(```|~~~)/.test(lines[i])) inFence = !inFence;
    if (inFence) continue;
    if (start === -1) {
      if (lines[i].trim() === heading) start = i + 1;
      continue;
    }
    const m = lines[i].match(/^(#+)\s/);
    if (m && m[1].length <= level) return lines.slice(start, i).join("\n").trim();
  }
  return start === -1 ? null : lines.slice(start).join("\n").trim();
}

function stripFrontMatter(md: string) {
  return md.replace(/^---\n[\s\S]*?\n---\n/, "");
}

/** Moves headings up or down, outside code fences. */
export function shiftHeadings(md: string, by: number) {
  if (!by) return md;
  let inFence = false;
  return md
    .split("\n")
    .map((line) => {
      if (/^(```|~~~)/.test(line)) inFence = !inFence;
      if (inFence) return line;
      return line.replace(/^(#{1,6})(\s)/, (_, hashes: string, sp: string) => "#".repeat(Math.min(6, Math.max(1, hashes.length + by))) + sp);
    })
    .join("\n");
}

/** Maps a link in `fromFile` to a site route, a GitHub URL or leaves it. */
export function rewriteHref(href: string, fromFile: string): string {
  if (/^(https?:|mailto:|tel:)/.test(href)) return href;
  if (href.startsWith("#")) {
    const anchor = href.slice(1);
    if (fromFile === "README.md" && readmeAnchors[anchor]) return readmeAnchors[anchor];
    return href;
  }
  const [target, anchor] = href.split("#");
  const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(fromFile), target)).replace(/^\.\//, "");
  if (resolved === "README.md" && anchor && readmeAnchors[anchor]) return readmeAnchors[anchor];
  const route = fileRoutes[resolved];
  if (route) return route + (anchor && !route.includes("#") ? `#${anchor}` : "");
  if (resolved.startsWith("..")) return href;
  const isDir = !path.posix.extname(resolved) || resolved.endsWith("/");
  return `${REPO}/${isDir ? "tree" : "blob"}/main/${resolved.replace(/\/$/, "")}${anchor ? `#${anchor}` : ""}`;
}

export interface Heading {
  depth: number;
  text: string;
  id: string;
}

export interface Rendered {
  html: string;
  headings: Heading[];
}

/** Renders Markdown from the repository. Content is the project's own, so raw HTML is kept. */
export function renderMarkdown(md: string, fromFile: string): Rendered {
  const headings: Heading[] = [];
  const used = new Map<string, number>();
  const marked = new Marked({ gfm: true });
  marked.use({
    walkTokens(token) {
      if (token.type === "link") (token as Tokens.Link).href = rewriteHref((token as Tokens.Link).href, fromFile);
      if (token.type === "image") {
        const img = token as Tokens.Image;
        if (!/^https?:/.test(img.href)) {
          const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(fromFile), img.href));
          img.href = `https://raw.githubusercontent.com/manziosee/atlas/main/${resolved}`;
        }
      }
    },
    renderer: {
      heading({ tokens, depth }) {
        const inner = this.parser.parseInline(tokens);
        const plain = inner.replace(/<[^>]+>/g, "");
        let id = slugify(plain);
        const n = used.get(id) ?? 0;
        used.set(id, n + 1);
        if (n) id = `${id}-${n}`;
        if (depth >= 2 && depth <= 3) headings.push({ depth, text: plain.replace(/&amp;/g, "&").replace(/&#39;/g, "'").replace(/&quot;/g, '"'), id });
        return `<h${depth} id="${id}"><a class="anchor" href="#${id}" aria-hidden="true">#</a>${inner}</h${depth}>\n`;
      },
      link({ href, title, tokens }) {
        const text = this.parser.parseInline(tokens);
        const external = /^https?:/.test(href);
        return `<a href="${href}"${title ? ` title="${title}"` : ""}${external ? ' target="_blank" rel="noreferrer"' : ""}>${text}</a>`;
      },
      table(token) {
        // Wide tables scroll inside the page instead of widening it.
        const header = token.header.map((cell) => `<th${cell.align ? ` style="text-align:${cell.align}"` : ""}>${this.parser.parseInline(cell.tokens)}</th>`).join("");
        const rows = token.rows
          .map((row) => `<tr>${row.map((cell) => `<td${cell.align ? ` style="text-align:${cell.align}"` : ""}>${this.parser.parseInline(cell.tokens)}</td>`).join("")}</tr>`)
          .join("");
        return `<div class="table-wrap"><table><thead><tr>${header}</tr></thead><tbody>${rows}</tbody></table></div>\n`;
      },
    },
  });
  const html = marked.parse(md, { async: false }) as string;
  return { html, headings };
}

/** Markdown for a page, assembled from its sources. Missing sources are skipped. */
export function pageMarkdown(page: Pick<DocPage, "sources">): { md: string; files: string[] } {
  const parts: string[] = [];
  const files: string[] = [];
  for (const src of page.sources) {
    const raw = read(src.file);
    if (raw === null) continue;
    let md = stripFrontMatter(raw);
    if ("section" in src) {
      const body = extractSection(md, src.section);
      if (body === null) continue;
      const title = src.section.replace(/^#+\s*/, "");
      md = `${src.section.match(/^#+/)![0]} ${title}\n\n${body}`;
      // The page title replaces a single section's heading.
      if (page.sources.length === 1) md = body;
    }
    md = shiftHeadings(md, src.shift ?? 0);
    // A file's own top-level title is redundant with the page title.
    if (page.sources.length === 1) md = md.replace(/^# .*\n+/, "");
    parts.push(md);
    files.push(src.file);
  }
  return { md: parts.join("\n\n"), files };
}

export function renderPage(page: DocPage): Rendered & { files: string[] } {
  const { md, files } = pageMarkdown(page);
  // Links resolve relative to the first source file.
  return { ...renderMarkdown(md, files[0] ?? "README.md"), files };
}

export function renderFile(file: string, opts: { dropTitle?: boolean } = {}): (Rendered & { file: string }) | null {
  const raw = read(file);
  if (raw === null) return null;
  let md = stripFrontMatter(raw);
  if (opts.dropTitle) md = md.replace(/^# .*\n+/, "");
  return { ...renderMarkdown(md, file), file };
}

export function renderSection(file: string, heading: string): Rendered | null {
  const raw = read(file);
  const body = raw && extractSection(raw, heading);
  return body ? renderMarkdown(body, file) : null;
}

export function editUrl(file: string) {
  return `${REPO}/blob/main/${file}`;
}

export function readRepoFile(file: string) {
  return read(file);
}
