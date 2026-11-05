"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Menu, X } from "lucide-react";
import { Wordmark } from "@/components/logo";
import { cn } from "@/lib/format";

const links = [
  { href: "#how", label: "How it works" },
  { href: "#features", label: "Features" },
  { href: "#demo", label: "Live demo" },
  { href: "#quick-start", label: "Quick start" },
  { href: "#docs", label: "Docs" },
];

/** Floating pill navigation: centred, never touching the screen edges. */
export function SiteNav() {
  const [open, setOpen] = useState(false);
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 12);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  return (
    <header className="pointer-events-none fixed inset-x-0 top-3 z-50 flex justify-center px-4 sm:top-5">
      <nav
        aria-label="Main"
        className={cn(
          "glass pointer-events-auto w-full max-w-3xl rounded-[1.4rem] border border-line-strong px-2.5 py-2 transition-shadow sm:rounded-full",
          scrolled && "shadow-[0_12px_40px_-12px_rgb(0_0_0/0.8)]",
        )}
      >
        <div className="flex items-center gap-2">
          <Link href="/" className="rounded-full px-2 py-1" aria-label="Atlas home">
            <Wordmark />
          </Link>
          <ul className="mx-auto hidden items-center gap-1 md:flex">
            {links.map((l) => (
              <li key={l.href}>
                <a href={l.href} className="rounded-full px-3 py-1.5 text-[0.82rem] text-muted transition-colors hover:bg-white/5 hover:text-fg">
                  {l.label}
                </a>
              </li>
            ))}
          </ul>
          <Link href="/connect/" className="btn btn-outline ml-auto hidden !py-1.5 !text-[0.82rem] sm:inline-flex md:ml-0">
            Open console
          </Link>
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            className="ml-auto rounded-full p-2 text-muted hover:bg-white/5 hover:text-fg sm:ml-0 md:hidden"
            aria-expanded={open}
            aria-controls="mobile-nav"
            aria-label={open ? "Close menu" : "Open menu"}
          >
            {open ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
          </button>
        </div>
        {open && (
          <ul id="mobile-nav" className="mt-2 grid gap-1 border-t border-line px-1 pb-1 pt-2 md:hidden">
            {links.map((l) => (
              <li key={l.href}>
                <a href={l.href} onClick={() => setOpen(false)} className="block rounded-xl px-3 py-2 text-sm text-muted hover:bg-white/5 hover:text-fg">
                  {l.label}
                </a>
              </li>
            ))}
            <li>
              <Link href="/connect/" className="btn btn-lime mt-1 w-full">
                Open console
              </Link>
            </li>
          </ul>
        )}
      </nav>
    </header>
  );
}
