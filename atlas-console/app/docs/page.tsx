import type { Metadata } from "next";
import Link from "next/link";
import { ArrowRight, FileCode2 } from "lucide-react";
import { docGroups, docPages } from "@/lib/content";

export const metadata: Metadata = { title: "Documentation", description: "Guides and reference for Atlas: quick start, console, integrations, operations and internals." };

export default function DocsIndex() {
  return (
    <article className="min-w-0 xl:col-span-2">
      <span className="eyebrow">Documentation</span>
      <h1 className="mt-4 text-3xl font-semibold tracking-tight sm:text-4xl">Learn, run and operate Atlas</h1>
      <p className="mt-3 max-w-2xl text-muted">
        Everything from your first node to running a global cluster. These pages are built from the documentation in the Atlas repository, so they always match
        the code they describe.
      </p>

      <Link href="/api/" className="panel mt-8 flex items-center gap-4 p-5 transition-colors hover:border-line-strong">
        <div className="icon-tile h-11 w-11">
          <FileCode2 className="h-5 w-5" />
        </div>
        <div className="flex-1">
          <p className="font-medium">API reference</p>
          <p className="text-sm text-muted">Every HTTP endpoint, parameter, response and error code, from the OpenAPI description.</p>
        </div>
        <ArrowRight className="h-4 w-4 text-muted" />
      </Link>

      {docGroups.map((group) => (
        <section key={group} className="mt-10" aria-labelledby={`g-${group}`}>
          <h2 id={`g-${group}`} className="text-lg font-medium">
            {group}
          </h2>
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            {docPages
              .filter((p) => p.group === group)
              .map((p) => (
                <Link key={p.slug} href={`/docs/${p.slug}/`} className="panel group p-5 transition-colors hover:border-line-strong">
                  <p className="flex items-center gap-1.5 font-medium">
                    {p.title} <ArrowRight className="h-4 w-4 opacity-0 transition-opacity group-hover:opacity-100" />
                  </p>
                  <p className="mt-1 text-sm text-muted">{p.description}</p>
                </Link>
              ))}
          </div>
        </section>
      ))}
    </article>
  );
}
