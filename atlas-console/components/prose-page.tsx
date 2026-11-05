import type { ReactNode } from "react";
import { Pencil } from "lucide-react";
import { PublicShell, TableOfContents } from "@/components/public-shell";
import { editUrl, type Heading } from "@/lib/content";

/** A single long-form page: title, rendered body, table of contents. */
export function ProsePage({
  eyebrow,
  title,
  intro,
  html,
  headings = [],
  source,
  children,
}: {
  eyebrow: string;
  title: string;
  intro?: ReactNode;
  html?: string;
  headings?: Heading[];
  source?: string;
  children?: ReactNode;
}) {
  return (
    <PublicShell>
      <div className="mx-auto grid max-w-6xl gap-10 px-4 pb-20 sm:px-6 xl:grid-cols-[minmax(0,1fr)_200px]">
        <article className="min-w-0">
          <span className="eyebrow">{eyebrow}</span>
          <h1 className="mt-4 text-3xl font-semibold tracking-tight sm:text-4xl">{title}</h1>
          {intro && <div className="mt-3 max-w-3xl text-muted">{intro}</div>}
          {children}
          {html && <div className="prose mt-8" dangerouslySetInnerHTML={{ __html: html }} />}
          {source && (
            <p className="mt-12 border-t border-line pt-6 text-sm">
              <a href={editUrl(source)} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 text-muted hover:text-fg">
                <Pencil className="h-3.5 w-3.5" /> Improve this page
              </a>
            </p>
          )}
        </article>
        <aside>
          <TableOfContents headings={headings} />
        </aside>
      </div>
    </PublicShell>
  );
}
