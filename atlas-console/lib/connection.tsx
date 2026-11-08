"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { AtlasClient, type NodeInfo, type Scope } from "./api";
import { DEMO_TOKEN, DEMO_URL } from "./demo";
import { track } from "./telemetry";

export interface Connection {
  mode: "node" | "demo";
  url: string;
  token: string;
  scopes: Scope[];
  info: NodeInfo;
}

interface ConnectionState {
  conn: Connection | null;
  client: AtlasClient | null;
  /** True once a saved session has been checked, so pages don't redirect early. */
  ready: boolean;
  connect: (url: string, token: string, opts?: { remember?: boolean }) => Promise<Connection>;
  connectDemo: () => Promise<Connection>;
  disconnect: () => void;
}

const STORAGE_KEY = "atlas-console:session";
const Ctx = createContext<ConnectionState | null>(null);

function save(conn: Connection | null) {
  try {
    if (conn) sessionStorage.setItem(STORAGE_KEY, JSON.stringify({ mode: conn.mode, url: conn.url, token: conn.token }));
    else sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage blocked (private mode, policy): stay in page memory only.
  }
}

export function ConnectionProvider({ children }: { children: ReactNode }) {
  const [conn, setConn] = useState<Connection | null>(null);
  const [ready, setReady] = useState(false);

  const open = useCallback(async (mode: Connection["mode"], url: string, token: string, remember: boolean) => {
    const client = new AtlasClient(url, token);
    const { info, scopes } = mode === "demo" ? { info: await client.node(), scopes: ["read"] as Scope[] } : await client.detectScopes();
    const next: Connection = { mode, url: client.base, token, scopes, info };
    setConn(next);
    // Count how many systems use Atlas: the node's version only, never its address.
    track({ type: "connect", kind: mode, version: info.version });
    save(remember || mode === "demo" ? next : null);
    return next;
  }, []);

  // Restore a session the user chose to keep for this tab.
  useEffect(() => {
    let saved: { mode: Connection["mode"]; url: string; token: string } | null = null;
    try {
      saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY) ?? "null");
    } catch {
      saved = null;
    }
    if (!saved) {
      setReady(true);
      return;
    }
    open(saved.mode, saved.url, saved.token, true)
      .catch(() => save(null))
      .finally(() => setReady(true));
  }, [open]);

  const value = useMemo<ConnectionState>(
    () => ({
      conn,
      ready,
      client: conn ? new AtlasClient(conn.url, conn.token) : null,
      connect: (url, token, opts) => open("node", url, token.trim(), !!opts?.remember),
      connectDemo: () => open("demo", DEMO_URL, DEMO_TOKEN, true),
      disconnect: () => {
        setConn(null);
        save(null);
      },
    }),
    [conn, ready, open],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useConnection() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useConnection must be used inside ConnectionProvider");
  return ctx;
}

export function can(conn: Connection | null, scope: Scope) {
  if (!conn || conn.mode === "demo") return scope === "read" && !!conn;
  return conn.scopes.includes("admin") || conn.scopes.includes(scope);
}
