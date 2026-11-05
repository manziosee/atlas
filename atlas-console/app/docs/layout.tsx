import type { ReactNode } from "react";
import { PublicShell } from "@/components/public-shell";
import { DocsNav, type NavGroup } from "@/components/docs-nav";
import { docGroups, docPages } from "@/lib/content";

export default function DocsLayout({ children }: { children: ReactNode }) {
  const groups: NavGroup[] = docGroups.map((group) => ({
    group,
    pages: docPages.filter((p) => p.group === group).map(({ slug, title }) => ({ slug, title })),
  }));
  return (
    <PublicShell>
      <div className="mx-auto grid max-w-7xl gap-8 px-4 pb-20 sm:px-6 lg:grid-cols-[220px_minmax(0,1fr)] xl:grid-cols-[220px_minmax(0,1fr)_200px]">
        <aside>
          <DocsNav groups={groups} />
        </aside>
        {children}
      </div>
    </PublicShell>
  );
}
