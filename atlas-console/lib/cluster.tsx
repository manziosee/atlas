"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AtlasError, type AtlasEvent, type Member, type Service } from "./api";
import { useConnection } from "./connection";

export type StreamStatus = "connecting" | "live" | "reconnecting" | "offline";

export interface FeedItem extends AtlasEvent {
  receivedAt: number;
}

interface ClusterState {
  members: Member[];
  services: Service[];
  feed: FeedItem[];
  loading: boolean;
  error: unknown;
  lastUpdated: number | null;
  stream: StreamStatus;
  streamError: unknown;
  refresh: () => Promise<void>;
  reconnect: () => void;
  clearFeed: () => void;
  /** Apply a service the user just changed, without waiting for the stream. */
  applyService: (service: Service) => void;
  removeService: (id: string) => void;
}

const Ctx = createContext<ClusterState | null>(null);
const FEED_LIMIT = 500;

function upsert<T extends { id: string }>(list: T[], item: T) {
  const index = list.findIndex((x) => x.id === item.id);
  if (index === -1) return [...list, item];
  const next = list.slice();
  next[index] = item;
  return next;
}

/**
 * Loads members and services, keeps them current from the event stream, and
 * falls back to polling while the stream is down.
 */
export function ClusterProvider({ children }: { children: ReactNode }) {
  const { client, disconnect } = useConnection();
  const [members, setMembers] = useState<Member[]>([]);
  const [services, setServices] = useState<Service[]>([]);
  const [feed, setFeed] = useState<FeedItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [stream, setStream] = useState<StreamStatus>("connecting");
  const [streamError, setStreamError] = useState<unknown>(null);
  const [streamEpoch, setStreamEpoch] = useState(0);
  const lastEventId = useRef<string | undefined>(undefined);

  const refresh = useCallback(async () => {
    if (!client) return;
    try {
      const [m, s] = await Promise.all([client.members(), client.services()]);
      setMembers(m);
      setServices(s);
      setError(null);
      setLastUpdated(Date.now());
    } catch (err) {
      setError(err);
      if (err instanceof AtlasError && err.code === "unauthorized") disconnect();
    } finally {
      setLoading(false);
    }
  }, [client, disconnect]);

  useEffect(() => {
    setLoading(true);
    void refresh();
  }, [refresh]);

  // Event stream with exponential backoff; resumes with Last-Event-ID.
  useEffect(() => {
    if (!client) return;
    const controller = new AbortController();
    let attempt = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const run = async () => {
      while (!controller.signal.aborted) {
        setStream(attempt === 0 ? "connecting" : "reconnecting");
        try {
          await client.streamEvents({
            lastEventId: lastEventId.current,
            signal: controller.signal,
            onOpen: () => {
              attempt = 0;
              setStream("live");
              setStreamError(null);
            },
            onEvent: (event, id) => {
              if (id) lastEventId.current = id;
              if (event.kind === "reset") {
                void refresh();
                return;
              }
              setFeed((f) => [{ ...event, receivedAt: Date.now() }, ...f].slice(0, FEED_LIMIT));
              setLastUpdated(Date.now());
              if (event.kind === "member" && event.member) {
                const member = event.member;
                setMembers((list) => (event.type === "reap" ? list.filter((m) => m.id !== member.id) : upsert(list, member)));
              } else if (event.kind === "service" && event.service) {
                const service = event.service;
                setServices((list) => (event.type === "service.deregistered" ? list.filter((s) => s.id !== service.id) : upsert(list, service)));
              }
            },
          });
        } catch (err) {
          if (controller.signal.aborted) return;
          setStreamError(err);
          if (err instanceof AtlasError && (err.code === "unauthorized" || err.code === "route_not_found")) {
            setStream("offline");
            return;
          }
        }
        if (controller.signal.aborted) return;
        attempt++;
        setStream(attempt > 6 ? "offline" : "reconnecting");
        const delay = Math.min(30_000, 1000 * 2 ** Math.min(attempt, 5));
        await new Promise<void>((resolve) => {
          timer = setTimeout(resolve, delay);
        });
        // Catch up on anything the stream may have missed.
        void refresh();
      }
    };
    void run();
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [client, refresh, streamEpoch]);

  // Poll while the stream is not live, and occasionally anyway.
  useEffect(() => {
    if (!client) return;
    const every = stream === "live" ? 60_000 : 15_000;
    const t = setInterval(() => void refresh(), every);
    return () => clearInterval(t);
  }, [client, stream, refresh]);

  const value = useMemo<ClusterState>(
    () => ({
      members,
      services,
      feed,
      loading,
      error,
      lastUpdated,
      stream,
      streamError,
      refresh,
      reconnect: () => setStreamEpoch((n) => n + 1),
      clearFeed: () => setFeed([]),
      applyService: (service) => setServices((list) => upsert(list, service)),
      removeService: (id) => setServices((list) => list.filter((s) => s.id !== id)),
    }),
    [members, services, feed, loading, error, lastUpdated, stream, streamError, refresh],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useCluster() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useCluster must be used inside ClusterProvider");
  return ctx;
}
