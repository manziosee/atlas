"use client";

import Link from "next/link";
import { Suspense, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ArrowLeft, Eye, KeyRound, Loader2, ShieldCheck } from "lucide-react";
import { Logo } from "@/components/logo";
import { Notice } from "@/components/console/ui";
import { CodeBlock } from "@/components/copy-button";
import { AtlasError, explain } from "@/lib/api";
import { useConnection } from "@/lib/connection";

function safeNext(next: string | null) {
  return next && next.startsWith("/") && !next.startsWith("//") && !next.startsWith("/connect") ? next : "/dashboard/";
}

function ConnectForm() {
  const { connect, connectDemo, conn, ready } = useConnection();
  const router = useRouter();
  const params = useSearchParams();
  const next = safeNext(params.get("next"));
  const [url, setUrl] = useState("http://localhost:8080");
  const [token, setToken] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState<{ text: string; cors?: boolean } | null>(null);
  const [busy, setBusy] = useState<"node" | "demo" | null>(null);
  const [origin, setOrigin] = useState("https://your-console.example");
  const autoTried = useRef(false);

  useEffect(() => setOrigin(window.location.origin), []);

  async function submit(nodeUrl: string, nodeToken: string, keep: boolean) {
    setError(null);
    const trimmed = nodeUrl.trim().replace(/\/+$/, "");
    if (!/^https?:\/\/[^/\s]+$/i.test(trimmed)) {
      setError({ text: "The node URL should look like http://host:8080 or https://atlas.example.com: a scheme and host, with no path." });
      return;
    }
    if (!nodeToken.trim()) {
      setError({ text: "Paste an API token. A read-only token is enough to view the dashboard." });
      return;
    }
    if (window.location.protocol === "https:" && trimmed.startsWith("http://") && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(:|$)/.test(trimmed)) {
      setError({ text: "This page is served over HTTPS, so browsers block plain http:// nodes other than localhost. Enable TLS on the node (ATLAS_TLS_ENABLED) or run the console locally." });
      return;
    }
    setBusy("node");
    try {
      await connect(trimmed, nodeToken, { remember: keep });
      router.push(next);
    } catch (err) {
      setError({ text: explain(err), cors: err instanceof AtlasError && err.code === "network" });
      setBusy(null);
    }
  }

  // One-click links from the node's dev-mode banner: /connect#node=...&token=...
  useEffect(() => {
    if (!ready || autoTried.current) return;
    autoTried.current = true;
    const hash = new URLSearchParams(window.location.hash.slice(1));
    const node = hash.get("node");
    const t = hash.get("token");
    if (!node && !t) return;
    history.replaceState(null, "", window.location.pathname + window.location.search); // keep the token out of history
    if (node) setUrl(node);
    if (t) setToken(t);
    if (node && t) void submit(node, t, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready]);

  return (
    <div className="relative flex min-h-dvh items-start justify-center px-4 py-10 sm:items-center">
      <div className="pointer-events-none absolute inset-0 -z-10 bg-[radial-gradient(50%_40%_at_50%_0%,rgb(47_211_181/0.18),transparent_70%),radial-gradient(40%_40%_at_90%_90%,rgb(182_242_122/0.10),transparent_70%)]" />
      <main id="main" className="w-full max-w-md">
        <Link href="/" className="mb-6 inline-flex items-center gap-2 text-sm text-muted hover:text-fg">
          <ArrowLeft className="h-4 w-4" /> Back to Atlas
        </Link>
        <div className="panel p-7 shadow-[0_30px_80px_-30px_rgb(47_211_181/0.35)] sm:p-8">
          <Logo className="h-10 w-10" />
          <h1 className="mt-5 text-2xl font-semibold tracking-tight">Connect to a node</h1>
          <p className="mt-2 text-sm text-muted">
            Atlas has no accounts; it uses API tokens. The console calls your node directly from this browser, and the token stays in page memory.
          </p>
          {conn && (
            <div className="mt-4">
              <Notice tone="info">
                You&apos;re connected to <span className="font-mono">{conn.mode === "demo" ? "the demo" : conn.url}</span>.{" "}
                <Link href="/dashboard/" className="underline">
                  Open the console
                </Link>
              </Notice>
            </div>
          )}
          <form
            className="mt-6 space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              void submit(url, token, remember);
            }}
          >
            <label className="block text-sm font-medium">
              Node URL
              <input className="field mt-1.5 font-mono" value={url} onChange={(e) => setUrl(e.target.value)} inputMode="url" autoComplete="url" spellCheck={false} required />
            </label>
            <label className="block text-sm font-medium">
              API token
              <input
                className="field mt-1.5 font-mono"
                type="password"
                autoComplete="off"
                spellCheck={false}
                placeholder="Paste a read, write or admin token"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                required
              />
            </label>
            <label className="flex items-start gap-2.5 text-sm text-muted">
              <input type="checkbox" className="mt-1 accent-[var(--color-lime)]" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
              <span>Keep me connected in this tab after a reload. The token is cleared when the tab closes.</span>
            </label>
            {error && (
              <Notice tone="error" title="Couldn't connect">
                {error.text}
              </Notice>
            )}
            {error?.cors && (
              <div className="space-y-2 text-xs text-muted">
                <p>Allow this console on the node, then restart it:</p>
                <CodeBlock title="node environment" code={`ATLAS_CORS_ORIGINS=${origin}`} />
              </div>
            )}
            <button className="btn btn-lime w-full" disabled={busy !== null}>
              {busy === "node" ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" /> Checking the token…
                </>
              ) : (
                <>
                  <KeyRound className="h-4 w-4" /> Connect
                </>
              )}
            </button>
          </form>

          <div className="mt-6 space-y-1.5 rounded-xl border border-line bg-bg-2 p-4 text-xs text-muted">
            <p className="mb-2 flex items-center gap-1.5 font-medium text-fg">
              <ShieldCheck className="h-3.5 w-3.5 text-lime" /> What each scope unlocks
            </p>
            <p>
              <span className="font-mono text-fg">read</span> — the cluster, services, history and live events
            </p>
            <p>
              <span className="font-mono text-fg">write</span> — register, edit, drain, heartbeat and deregister
            </p>
            <p>
              <span className="font-mono text-fg">admin</span> — credentials, backups and evicting failed nodes
            </p>
          </div>

          <button
            type="button"
            disabled={busy !== null}
            onClick={async () => {
              setBusy("demo");
              setError(null);
              try {
                await connectDemo();
                router.push(next);
              } catch (err) {
                setError({ text: explain(err) });
                setBusy(null);
              }
            }}
            className="btn btn-outline mt-4 w-full"
          >
            {busy === "demo" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eye className="h-4 w-4" />} Try the read-only demo instead
          </button>
          <p className="mt-3 text-center text-xs text-faint">Never paste a production token into a page you don&apos;t control.</p>
        </div>
      </main>
    </div>
  );
}

export default function ConnectPage() {
  return (
    <Suspense>
      <ConnectForm />
    </Suspense>
  );
}
