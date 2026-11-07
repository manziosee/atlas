"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState, type ReactNode } from "react";
import { Activity, BarChart3, BookOpen, Boxes, Eye, Globe2, LayoutDashboard, LogOut, Menu, Plug, RefreshCw, Search, Server, Shield, X } from "lucide-react";
import { Wordmark } from "@/components/logo";
import { can, useConnection } from "@/lib/connection";
import { useCluster, type StreamStatus } from "@/lib/cluster";
import { cn, relative } from "@/lib/format";
import { Spinner } from "./ui";

const links = [
  { href: "/dashboard/", label: "Overview", icon: LayoutDashboard },
  { href: "/nodes/", label: "Nodes", icon: Server },
  { href: "/topology/", label: "Cluster map", icon: Globe2 },
  { href: "/services/", label: "Services", icon: Boxes },
  { href: "/activity/", label: "Activity", icon: Activity },
  { href: "/lookup/", label: "Lookup tester", icon: Search },
  { href: "/integrations/", label: "Integrations", icon: Plug },
  { href: "/admin/analytics/", label: "Usage & reliability", icon: BarChart3, admin: true },
  { href: "/admin/", label: "Admin", icon: Shield, admin: true },
];

const streamLabel: Record<StreamStatus, { text: string; cls: string }> = {
  connecting: { text: "Connecting…", cls: "text-muted" },
  live: { text: "Live", cls: "text-ok" },
  reconnecting: { text: "Reconnecting…", cls: "text-warn" },
  offline: { text: "Stream offline · polling", cls: "text-bad" },
};

function useNow(every = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}

export function ConsoleShell({ children }: { children: ReactNode }) {
  const { conn, disconnect } = useConnection();
  const { stream, lastUpdated, refresh, reconnect } = useCluster();
  const [open, setOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const pathname = usePathname();
  const router = useRouter();
  const now = useNow();
  useEffect(() => setOpen(false), [pathname]);
  if (!conn) return null;

  const status = streamLabel[stream];
  const scopes = conn.mode === "demo" ? "read-only demo" : conn.scopes.includes("admin") ? "admin" : conn.scopes.includes("write") ? "read · write" : "read-only";

  return (
    <div className="min-h-dvh">
      <header className="glass sticky top-0 z-40 border-b border-line">
        <div className="flex h-14 items-center gap-2 px-3 sm:px-4">
          <button className="rounded-lg p-2 text-muted hover:bg-white/5 hover:text-fg lg:hidden" onClick={() => setOpen((o) => !o)} aria-label={open ? "Close navigation" : "Open navigation"} aria-expanded={open}>
            {open ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
          </button>
          <Link href="/" aria-label="Atlas home">
            <Wordmark />
          </Link>
          <div className="ml-auto flex items-center gap-1.5 text-xs sm:gap-3">
            <button
              type="button"
              onClick={stream === "offline" ? reconnect : undefined}
              className={cn("hidden items-center gap-1.5 rounded-full px-2 py-1 sm:flex", status.cls, stream === "offline" && "hover:bg-white/5")}
              title={stream === "offline" ? "Retry the live event stream" : "Live event stream status"}
            >
              <span className={cn("h-2 w-2 rounded-full bg-current", stream === "live" && "animate-pulse-dot")} aria-hidden />
              <span role="status">{status.text}</span>
            </button>
            <span className="hidden text-muted md:inline">Updated {relative(lastUpdated, now)}</span>
            <button
              onClick={async () => {
                setRefreshing(true);
                await refresh();
                setRefreshing(false);
              }}
              className="rounded-lg p-2 text-muted hover:bg-white/5 hover:text-fg"
              aria-label="Refresh data"
            >
              <RefreshCw className={cn("h-4 w-4", refreshing && "animate-spin")} />
            </button>
            <span className="hidden max-w-[260px] items-center gap-2 truncate rounded-full border border-line-strong px-3 py-1 font-mono text-muted lg:flex" title={conn.url}>
              {conn.mode === "demo" ? <Eye className="h-3.5 w-3.5 text-info" /> : null}
              <span className="truncate">{conn.mode === "demo" ? "demo cluster" : conn.url.replace(/^https?:\/\//, "")}</span>
              <span className="text-faint">·</span>
              <span className="text-lime">{scopes}</span>
            </span>
            <button
              onClick={() => {
                disconnect();
                router.push("/connect/");
              }}
              className="flex items-center gap-1.5 rounded-lg px-2 py-2 text-muted hover:bg-white/5 hover:text-fg"
            >
              <LogOut className="h-4 w-4" /> <span className="hidden sm:inline">Disconnect</span>
            </button>
          </div>
        </div>
      </header>
      <div className="flex">
        {open && <div className="fixed inset-0 top-14 z-20 bg-black/50 lg:hidden" onClick={() => setOpen(false)} aria-hidden />}
        <aside
          className={cn(
            "fixed bottom-0 left-0 top-14 z-30 w-64 border-r border-line bg-bg p-3 transition-transform lg:sticky lg:top-14 lg:h-[calc(100dvh-3.5rem)] lg:translate-x-0",
            open ? "translate-x-0" : "-translate-x-full",
          )}
        >
          <nav aria-label="Console" className="flex flex-col gap-1">
            {links.filter((link) => !link.admin || can(conn, "admin")).map(({ href, label, icon: Icon }) => {
              const active = pathname === href || pathname === href.slice(0, -1) || pathname.startsWith(href);
              return (
                <Link
                  key={href}
                  href={href}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "flex items-center gap-3 rounded-xl px-3 py-2 text-sm transition-colors",
                    active ? "bg-white/[0.06] font-medium text-fg" : "text-muted hover:bg-white/[0.04] hover:text-fg",
                  )}
                >
                  <Icon className={cn("h-4 w-4", active && "text-lime")} /> {label}
                </Link>
              );
            })}
          </nav>
          <Link href="/docs/" className="mt-3 flex items-center gap-3 rounded-xl px-3 py-2 text-sm text-muted hover:bg-white/[0.04] hover:text-fg">
            <BookOpen className="h-4 w-4" /> Docs &amp; API
          </Link>
          <div className="mt-6 space-y-3 rounded-xl border border-line p-3 text-xs text-muted">
            <p>
              Node <span className="font-mono text-fg">{conn.info.node.id}</span>
            </p>
            <p>
              Region <span className="font-mono text-fg">{conn.info.node.region || "—"}</span> · <span className="font-mono">{conn.info.version}</span>
            </p>
          </div>
          {conn.mode === "demo" && (
            <p className="mt-3 rounded-xl border border-info/35 bg-info/10 p-3 text-xs text-info">
              You&apos;re exploring the public demo with a read-only token. Actions that change services are turned off.
            </p>
          )}
        </aside>
        <main id="main" className="min-w-0 flex-1 p-4 sm:p-6 lg:p-8">
          {children}
        </main>
      </div>
    </div>
  );
}

export function ConsoleLoading() {
  return (
    <div className="grid min-h-dvh place-items-center">
      <Spinner label="Opening the console" />
    </div>
  );
}
