import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { ArrowLeft, ArrowRight, Pencil } from "lucide-react";
import { TableOfContents } from "@/components/public-shell";
import { docPages, editUrl, renderPage } from "@/lib/content";

export const dynamicParams = false;

export function generateStaticParams() {
  return docPages.map((p) => ({ slug: p.slug }));
}

export async function generateMetadata({ params }: { params: Promise<{ slug: string }> }): Promise<Metadata> {
  const { slug } = await params;
  const page = docPages.find((p) => p.slug === slug);
  return page ? { title: page.title, description: page.description } : {};
}

export default async function DocPage({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const index = docPages.findIndex((p) => p.slug === slug);
  if (index === -1) notFound();
  const page = docPages[index];
  const { html, headings, files } = renderPage(page);
  if (!files.length) notFound();
  const prev = docPages[index - 1];
  const next = docPages[index + 1];

  return (
    <>
      <article className="min-w-0">
        <p className="text-sm text-muted">{page.group}</p>
        <h1 className="mt-1 text-3xl font-semibold tracking-tight sm:text-4xl">{page.title}</h1>
        <p className="mt-2 text-muted">{page.description}</p>
        <div className="prose mt-8" dangerouslySetInnerHTML={{ __html: html }} />

        <div className="mt-12 flex flex-wrap items-center justify-between gap-3 border-t border-line pt-6 text-sm">
          <a href={editUrl(files[0])} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 text-muted hover:text-fg">
            <Pencil className="h-3.5 w-3.5" /> Improve this page
          </a>
          <span className="text-xs text-faint">Source: {files.join(", ")}</span>
        </div>
        <nav aria-label="More docs" className="mt-6 grid gap-3 sm:grid-cols-2">
          {prev ? (
            <Link href={`/docs/${prev.slug}/`} className="panel p-4 hover:border-line-strong">
              <span className="flex items-center gap-1 text-xs text-muted">
                <ArrowLeft className="h-3.5 w-3.5" /> Previous
              </span>
              <span className="mt-1 block font-medium">{prev.title}</span>
            </Link>
          ) : (
            <span />
          )}
          {next && (
            <Link href={`/docs/${next.slug}/`} className="panel p-4 text-right hover:border-line-strong">
              <span className="flex items-center justify-end gap-1 text-xs text-muted">
                Next <ArrowRight className="h-3.5 w-3.5" />
              </span>
              <span className="mt-1 block font-medium">{next.title}</span>
            </Link>
          )}
        </nav>
      </article>
      <aside>
        <TableOfContents headings={headings} />
      </aside>
    </>
  );
}
