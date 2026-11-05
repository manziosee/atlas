import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { PublicShell } from "@/components/public-shell";
import { CodeBlock } from "@/components/copy-button";
import { loadApiSpec, type Endpoint } from "@/lib/openapi";
import { editUrl } from "@/lib/content";

export const metadata: Metadata = { title: "API reference", description: "Every Atlas HTTP endpoint, parameter, response and error code." };

const methodTone: Record<string, string> = {
  GET: "text-info border-info/40 bg-info/10",
  POST: "text-ok border-ok/40 bg-ok/10",
  PUT: "text-warn border-warn/40 bg-warn/10",
  PATCH: "text-teal border-teal/40 bg-teal/10",
  DELETE: "text-bad border-bad/40 bg-bad/10",
};

const scopeTone: Record<Endpoint["scope"], string> = {
  none: "text-muted border-line-strong",
  read: "text-info border-info/35",
  write: "text-warn border-warn/35",
  admin: "text-bad border-bad/40",
};

function Method({ m }: { m: string }) {
  return <span className={`inline-block w-16 rounded-md border py-0.5 text-center font-mono text-[0.7rem] font-semibold ${methodTone[m] ?? ""}`}>{m}</span>;
}

function curlFor(e: Endpoint) {
  const path = e.path.replace(/\{(\w+)\}/g, (_, p) => (p === "name" ? "payments" : "<" + p + ">"));
  const auth = e.public ? "" : ' -H "Authorization: Bearer $ATLAS_TOKEN"';
  const body = e.body === "ServiceSpec" ? ` \\\n  -H "Content-Type: application/json" \\\n  -d '{"name":"payments","address":"10.0.0.8:8080","health_check":"http://10.0.0.8:8080/healthz"}'` : e.body === "ServicePatch" ? ` \\\n  -H "Content-Type: application/json" -d '{"draining":true}'` : "";
  const method = e.method === "GET" ? "" : ` -X ${e.method}`;
  return `curl${method}${auth} "$ATLAS${path}"${body}`;
}

export default function ApiReference() {
  const spec = loadApiSpec();
  if (!spec) notFound();
  const byTag = spec.tags.map((tag) => ({ tag, endpoints: spec.endpoints.filter((e) => e.tag === tag) })).filter((g) => g.endpoints.length);

  return (
    <PublicShell>
      <div className="mx-auto grid max-w-7xl gap-8 px-4 pb-20 sm:px-6 lg:grid-cols-[240px_minmax(0,1fr)]">
        <aside>
          <nav aria-label="Endpoints" className="sticky top-28 hidden max-h-[calc(100dvh-8rem)] overflow-y-auto pr-2 lg:block">
            <a href="#authentication" className="block rounded-lg px-2.5 py-1.5 text-sm text-muted hover:text-fg">
              Authentication
            </a>
            {byTag.map((g) => (
              <div key={g.tag} className="mt-4">
                <p className="text-xs font-medium uppercase tracking-wider text-faint">{g.tag}</p>
                <ul className="mt-1.5 space-y-0.5">
                  {g.endpoints.map((e) => (
                    <li key={e.id}>
                      <a href={`#${e.id}`} className="flex items-center gap-2 rounded-lg px-2.5 py-1 text-xs text-muted hover:bg-white/[0.04] hover:text-fg">
                        <span className="w-12 shrink-0 font-mono text-[0.65rem] font-semibold">{e.method}</span>
                        <span className="truncate font-mono">{e.path}</span>
                      </a>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
            <div className="mt-4 space-y-0.5">
              <a href="#schemas" className="block rounded-lg px-2.5 py-1.5 text-sm text-muted hover:text-fg">
                Schemas
              </a>
              <a href="#errors" className="block rounded-lg px-2.5 py-1.5 text-sm text-muted hover:text-fg">
                Error codes
              </a>
            </div>
          </nav>
        </aside>

        <article className="min-w-0">
          <span className="eyebrow">API reference · v{spec.version}</span>
          <h1 className="mt-4 text-3xl font-semibold tracking-tight sm:text-4xl">{spec.title}</h1>
          <p className="mt-3 max-w-3xl text-muted">{spec.description}</p>
          <div className="mt-5 flex flex-wrap gap-3 text-sm">
            <a href={editUrl("api/v1/openapi.yaml")} target="_blank" rel="noreferrer" className="btn btn-outline !py-1.5 text-xs">
              OpenAPI source
            </a>
            <Link href="/docs/registry-api/" className="btn btn-outline !py-1.5 text-xs">
              Registry guide
            </Link>
            <Link href="/connect/" className="btn btn-lime !py-1.5 text-xs">
              Try it in the console
            </Link>
          </div>

          <section id="authentication" className="panel mt-10 scroll-mt-28 p-6">
            <h2 className="text-xl font-semibold">Authentication</h2>
            <p className="mt-2 text-sm leading-relaxed text-muted">{spec.auth}</p>
            <div className="mt-4 grid gap-3 sm:grid-cols-3 text-sm">
              {(
                [
                  ["read", "GET requests: members, services, lookups, history, events"],
                  ["write", "POST, PUT, PATCH, DELETE on services and heartbeats"],
                  ["admin", "/v1/admin routes; holds every scope"],
                ] as const
              ).map(([s, d]) => (
                <div key={s} className="rounded-xl border border-line p-3">
                  <span className={`rounded-full border px-2 py-0.5 font-mono text-xs ${scopeTone[s]}`}>{s}</span>
                  <p className="mt-2 text-muted">{d}</p>
                </div>
              ))}
            </div>
            {spec.servers.length > 0 && (
              <div className="mt-5">
                <CodeBlock title="servers" code={spec.servers.map((s) => `export ATLAS=${s.url}   # ${s.description}`).join("\n")} />
              </div>
            )}
          </section>

          {byTag.map((g) => (
            <section key={g.tag} className="mt-12" aria-labelledby={`tag-${g.tag}`}>
              <h2 id={`tag-${g.tag}`} className="text-xl font-semibold capitalize">
                {g.tag}
              </h2>
              <div className="mt-4 space-y-4">
                {g.endpoints.map((e) => (
                  <div key={e.id} id={e.id} className="panel scroll-mt-28 p-5">
                    <div className="flex flex-wrap items-center gap-3">
                      <Method m={e.method} />
                      <code className="break-all font-mono text-sm text-fg">{e.path}</code>
                      <span className={`ml-auto rounded-full border px-2 py-0.5 font-mono text-[0.7rem] ${scopeTone[e.scope]}`}>{e.scope === "none" ? "public" : `${e.scope} scope`}</span>
                    </div>
                    <p className="mt-3 font-medium">{e.summary}</p>
                    {e.description && <p className="mt-1.5 text-sm leading-relaxed text-muted">{e.description}</p>}

                    {e.params.length > 0 && (
                      <div className="mt-4 overflow-x-auto rounded-xl border border-line">
                        <table className="w-full min-w-[520px] text-sm">
                          <thead className="bg-white/[0.03] text-left text-xs text-faint">
                            <tr>
                              <th className="px-3 py-2 font-normal">Parameter</th>
                              <th className="px-3 py-2 font-normal">In</th>
                              <th className="px-3 py-2 font-normal">Type</th>
                              <th className="px-3 py-2 font-normal">Description</th>
                            </tr>
                          </thead>
                          <tbody className="divide-y divide-line">
                            {e.params.map((p) => (
                              <tr key={`${p.in}-${p.name}`}>
                                <td className="px-3 py-2 font-mono">
                                  {p.name}
                                  {p.required && <span className="ml-1 text-bad">*</span>}
                                </td>
                                <td className="px-3 py-2 text-muted">{p.in}</td>
                                <td className="px-3 py-2 font-mono text-xs text-muted">{p.type}</td>
                                <td className="px-3 py-2 text-muted">{p.description}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                    {e.body && (
                      <p className="mt-3 text-sm text-muted">
                        Request body: <a href={`#schema-${e.body}`} className="font-mono text-teal hover:underline">{e.body}</a>
                      </p>
                    )}
                    <ul className="mt-3 flex flex-wrap gap-2 text-xs">
                      {e.responses.map((r) => (
                        <li key={r.status} className="rounded-lg border border-line px-2 py-1 text-muted" title={r.description}>
                          <span className={`font-mono font-semibold ${r.status.startsWith("2") ? "text-ok" : "text-warn"}`}>{r.status}</span> {r.description}
                          {r.schema && r.schema !== "Error" && <span className="font-mono"> · {r.schema}</span>}
                        </li>
                      ))}
                    </ul>
                    <details className="mt-3">
                      <summary className="cursor-pointer text-xs text-muted hover:text-fg">Example request</summary>
                      <div className="mt-2">
                        <CodeBlock title="curl" code={curlFor(e)} />
                      </div>
                    </details>
                  </div>
                ))}
              </div>
            </section>
          ))}

          <section id="schemas" className="mt-14 scroll-mt-28">
            <h2 className="text-xl font-semibold">Schemas</h2>
            <div className="mt-4 space-y-4">
              {spec.schemas.map((s) => (
                <div key={s.name} id={`schema-${s.name}`} className="panel scroll-mt-28 p-5">
                  <p className="font-mono font-medium">{s.name}</p>
                  {s.description && <p className="mt-1 text-sm text-muted">{s.description}</p>}
                  {s.enum && <p className="mt-2 font-mono text-xs text-muted">{s.enum.join(" | ")}</p>}
                  {s.props.length > 0 && (
                    <div className="mt-3 overflow-x-auto rounded-xl border border-line">
                      <table className="w-full min-w-[480px] text-sm">
                        <tbody className="divide-y divide-line">
                          {s.props.map((p) => (
                            <tr key={p.name}>
                              <td className="w-48 px-3 py-2 align-top font-mono">
                                {p.name}
                                {p.required && <span className="ml-1 text-bad">*</span>}
                              </td>
                              <td className="w-44 px-3 py-2 align-top font-mono text-xs text-muted">{p.type}</td>
                              <td className="px-3 py-2 align-top text-muted">{p.description}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </section>

          <section id="errors" className="mt-14 scroll-mt-28">
            <h2 className="text-xl font-semibold">Error codes</h2>
            <p className="mt-2 text-sm text-muted">Every error response is JSON with a stable <span className="font-mono">code</span>, a readable <span className="font-mono">error</span> and a <span className="font-mono">request_id</span> that matches the node&apos;s access log. Branch on the code, never on the message.</p>
            <div className="mt-4 flex flex-wrap gap-2">
              {spec.errorCodes.map((c) => (
                <span key={c} className="rounded-lg border border-line px-2 py-1 font-mono text-xs text-muted">
                  {c}
                </span>
              ))}
            </div>
          </section>
        </article>
      </div>
    </PublicShell>
  );
}
