import type { ReactNode } from "react";
import { SiteNav } from "@/components/landing/site-nav";
import { SiteFooter } from "@/components/site-footer";

/** Public pages: the floating nav, content that scrolls under it, the footer. */
export function PublicShell({ children }: { children: ReactNode }) {
  return (
    <>
      <SiteNav />
      <div className="pointer-events-none absolute inset-x-0 top-0 -z-10 h-[420px] bg-[radial-gradient(50%_60%_at_50%_0%,rgb(47_211_181/0.12),transparent_70%)]" />
      <main id="main" className="min-h-[70vh] pt-28 sm:pt-32">
        {children}
      </main>
      <SiteFooter />
    </>
  );
}

export function TableOfContents({ headings }: { headings: { depth: number; text: string; id: string }[] }) {
  if (headings.length < 2) return null;
  return (
    <nav aria-label="On this page" className="sticky top-28 hidden max-h-[calc(100dvh-8rem)] overflow-y-auto text-sm xl:block">
      <p className="text-xs font-medium uppercase tracking-wider text-faint">On this page</p>
      <ul className="mt-3 space-y-1.5 border-l border-line">
        {headings.map((h) => (
          <li key={h.id}>
            <a href={`#${h.id}`} className={`-ml-px block border-l border-transparent py-0.5 text-muted hover:border-lime hover:text-fg ${h.depth === 3 ? "pl-6 text-xs" : "pl-3"}`}>
              {h.text}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
