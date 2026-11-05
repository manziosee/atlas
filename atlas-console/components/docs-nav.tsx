"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/format";

export interface NavGroup {
  group: string;
  pages: { slug: string; title: string }[];
}

const extra = [
  { href: "/api/", label: "API reference" },
  { href: "/security/", label: "Security" },
  { href: "/contributing/", label: "Contributing" },
];

function Links({ groups }: { groups: NavGroup[] }) {
  const pathname = usePathname();
  const active = (href: string) => pathname === href || pathname === href.replace(/\/$/, "");
  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <div key={g.group}>
          <p className="text-xs font-medium uppercase tracking-wider text-faint">{g.group}</p>
          <ul className="mt-2 space-y-0.5">
            {g.pages.map((p) => {
              const href = `/docs/${p.slug}/`;
              return (
                <li key={p.slug}>
                  <Link
                    href={href}
                    aria-current={active(href) ? "page" : undefined}
                    className={cn("block rounded-lg px-2.5 py-1.5 text-sm", active(href) ? "bg-lime/10 text-lime" : "text-muted hover:bg-white/[0.04] hover:text-fg")}
                  >
                    {p.title}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
      <div>
        <p className="text-xs font-medium uppercase tracking-wider text-faint">Reference</p>
        <ul className="mt-2 space-y-0.5">
          {extra.map((l) => (
            <li key={l.href}>
              <Link href={l.href} className={cn("block rounded-lg px-2.5 py-1.5 text-sm", active(l.href) ? "bg-lime/10 text-lime" : "text-muted hover:bg-white/[0.04] hover:text-fg")}>
                {l.label}
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** Sidebar on large screens; a collapsible menu on phones. */
export function DocsNav({ groups }: { groups: NavGroup[] }) {
  return (
    <>
      <details className="panel mb-6 p-4 lg:hidden">
        <summary className="cursor-pointer text-sm font-medium">Documentation menu</summary>
        <div className="mt-4">
          <Links groups={groups} />
        </div>
      </details>
      <nav aria-label="Documentation" className="sticky top-28 hidden max-h-[calc(100dvh-8rem)] overflow-y-auto pr-2 lg:block">
        <Links groups={groups} />
      </nav>
    </>
  );
}
