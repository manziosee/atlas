// Typed client for the Atlas node HTTP API (api/v1/openapi.yaml).
// The console calls the node straight from the browser with a bearer token;
// nothing passes through a console server.

import { SSEParser } from "./sse";

export type Scope = "read" | "write" | "admin";
export type Health = "unknown" | "healthy" | "unhealthy";
export type MemberState = "alive" | "suspect" | "dead" | "left";

export interface Member {
  id: string;
  addr: string;
  region: string;
  state: MemberState;
  incarnation: number;
  since?: string;
  api_addr?: string;
}

export interface NodeInfo {
  node: Member;
  version: string;
  commit: string;
}

export interface Service {
  id: string;
  name: string;
  node_id: string;
  address: string;
  region: string;
  version?: string;
  tags?: string[];
  health_check?: string;
  health: Health;
  revision: number;
  ttl_seconds?: number;
  check_interval_seconds?: number;
  unhealthy_threshold?: number;
  healthy_threshold?: number;
  draining?: boolean;
  updated_at: string;
  checked_at?: string;
  heartbeat_at?: string;
}

export interface ServiceSpec {
  id?: string;
  name: string;
  address: string;
  version?: string;
  tags?: string[];
  health_check?: string;
  ttl_seconds?: number;
  check_interval_seconds?: number;
  unhealthy_threshold?: number;
  healthy_threshold?: number;
  draining?: boolean;
}

export type ServicePatch = Partial<Omit<ServiceSpec, "id">>;

export interface HistoryEntry {
  seq: number;
  service_id: string;
  name: string;
  revision: number;
  change: "registered" | "updated" | "health" | "deregistered";
  at: string;
  service?: Service;
}

export interface HistoryPage {
  count: number;
  entries: HistoryEntry[];
  next_before?: number;
}

export interface PeerLatency {
  id: string;
  region?: string;
  state: MemberState;
  rtt_ms?: number;
}

export interface NodeTopology {
  node: Member;
  location?: { lat: number; lon: number };
  version: string;
  peers: PeerLatency[];
  at: string;
}

export interface ClusterTopology {
  nodes: { id: string; view?: NodeTopology; error?: string }[];
  truncated: boolean;
  at: string;
}

export interface AtlasEvent {
  id: string;
  type: string;
  kind: "member" | "service" | "reset";
  at: string;
  member?: Member;
  service?: Service;
}

export interface Credential {
  id: string;
  scopes: Scope[];
  expires_at?: string;
  source: "static" | "scoped";
  expired: boolean;
  current: boolean;
}

export interface AnalyticsDay {
  date: string;
  page_views: number;
  unique_visitors: number;
}

export interface AnalyticsPage {
  page: string;
  page_views: number;
}

export interface AdminAnalytics {
  days: number;
  node_id: string;
  nodes_included: number;
  node_errors: { node_id: string; error: string }[];
  daily: AnalyticsDay[];
  pages: AnalyticsPage[];
  api_requests_since_start: number;
  api_errors_since_start: number;
  api_requests_by_route: { route: string; count: number; errors: number }[];
}

export interface LookupOptions {
  region?: string;
  key?: string;
  tags?: string[];
  stale?: "" | "include" | "prefer-live" | "exclude";
  latency?: "" | "nearest" | "off";
  tolerance?: string;
}

/** An API or network failure, with a plain-language explanation. */
export class AtlasError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public requestId?: string,
    public ownerNode?: string,
  ) {
    super(message);
    this.name = "AtlasError";
  }

  /** What went wrong and what to do about it, for people rather than logs. */
  get friendly(): string {
    return explain(this);
  }
}

export function explain(err: unknown): string {
  if (!(err instanceof AtlasError)) return err instanceof Error ? err.message : String(err);
  switch (err.code) {
    case "network":
      return "Couldn't reach the node. Check the URL and that the node is running. If it is, the node must allow this page's origin: set ATLAS_CORS_ORIGINS on the node (see the Connect page).";
    case "unauthorized":
      return "The node didn't accept this token. It may be mistyped, revoked or expired. Connect again with a valid token.";
    case "insufficient_scope":
      return `Your token can't do this: ${err.message.replace(/^credential lacks/, "it lacks")}. Ask an operator for a token with that scope.`;
    case "not_found":
      return "That service no longer exists. It may have expired or been deregistered; refresh to see the current list.";
    case "route_not_found":
      return "This node doesn't support that feature. Upgrade the node to a newer Atlas version.";
    case "validation_failed":
    case "invalid_json":
      return `The node rejected the input: ${err.message}.`;
    case "not_owner":
    case "owner_alive":
      return `Only the owning node${err.ownerNode ? ` (${err.ownerNode})` : ""} can change this service, and it couldn't be reached through this node. Connect to that node, or use an admin token to delete it from here.`;
    case "owner_unreachable":
      return `The owning node${err.ownerNode ? ` (${err.ownerNode})` : ""} didn't answer. Try again shortly.`;
    case "member_alive":
      return "That node is alive, so it can't be evicted; it would rejoin immediately. Only suspect, dead or left nodes can be evicted.";
    case "precondition_failed":
      return "Someone changed this service while you were editing. Reload it and apply your change again.";
    case "no_ttl":
      return "This service uses a health check, not a TTL, so it doesn't take heartbeats.";
    case "rate_limited":
      return "The node is rate-limiting requests from this address. Wait a moment and try again.";
    case "no_healthy_instance":
      return "No instance qualifies: every match is draining, unhealthy, or filtered out by region, tags or the stale-record policy.";
    case "unavailable":
      return "The node's registry is temporarily unavailable. Try again shortly.";
    default:
      return err.message || `The node answered ${err.status}.`;
  }
}

function trimBase(base: string) {
  return base.replace(/\/+$/, "");
}

export class AtlasClient {
  readonly base: string;

  constructor(
    base: string,
    private token: string,
  ) {
    this.base = trimBase(base);
  }

  private async send(method: string, path: string, init: { body?: unknown; query?: URLSearchParams; headers?: Record<string, string> } = {}): Promise<Response> {
    const query = init.query && [...init.query.keys()].length ? `?${init.query}` : "";
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, Accept: "application/json", ...init.headers };
    let body: string | undefined;
    if (init.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(init.body);
    }
    let response: Response;
    try {
      response = await fetch(`${this.base}${path}${query}`, { method, headers, body, cache: "no-store" });
    } catch (cause) {
      throw new AtlasError(0, "network", cause instanceof Error ? cause.message : "network error");
    }
    if (!response.ok) throw await toError(response);
    return response;
  }

  private async json<T>(method: string, path: string, init?: Parameters<AtlasClient["send"]>[2]): Promise<T> {
    const response = await this.send(method, path, init);
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  node() {
    return this.json<NodeInfo>("GET", "/v1/node");
  }

  async members(): Promise<Member[]> {
    return (await this.json<{ members: Member[] }>("GET", "/v1/members")).members ?? [];
  }

  /** Every service, following pages. */
  async services(): Promise<Service[]> {
    const all: Service[] = [];
    let cursor = "";
    for (let page = 0; page < 100; page++) {
      const query = new URLSearchParams({ limit: "1000" });
      if (cursor) query.set("cursor", cursor);
      const result = await this.json<{ services: Service[]; next_cursor?: string }>("GET", "/v1/services", { query });
      all.push(...(result.services ?? []));
      if (!result.next_cursor) break;
      cursor = result.next_cursor;
    }
    return all;
  }

  service(id: string) {
    return this.json<Service>("GET", `/v1/services/${encodeURIComponent(id)}`);
  }

  register(spec: ServiceSpec) {
    return this.json<Service>("POST", "/v1/services", { body: spec });
  }

  update(id: string, patch: ServicePatch, revision?: number) {
    return this.json<Service>("PATCH", `/v1/services/${encodeURIComponent(id)}`, {
      body: patch,
      headers: revision ? { "If-Match": `"${revision}"` } : undefined,
    });
  }

  setDraining(id: string, draining: boolean) {
    return this.update(id, { draining });
  }

  heartbeat(id: string) {
    return this.json<Service>("POST", `/v1/services/${encodeURIComponent(id)}/heartbeat`);
  }

  deregister(id: string) {
    return this.json<void>("DELETE", `/v1/services/${encodeURIComponent(id)}`);
  }

  history(id: string, before?: number, limit = 50) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (before) query.set("before", String(before));
    return this.json<HistoryPage>("GET", `/v1/services/${encodeURIComponent(id)}/history`, { query });
  }

  recentHistory(name?: string, before?: number, limit = 50) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (name) query.set("name", name);
    if (before) query.set("before", String(before));
    return this.json<HistoryPage>("GET", "/v1/history", { query });
  }

  lookup(name: string, opts: LookupOptions = {}) {
    const query = new URLSearchParams();
    if (opts.region) query.set("region", opts.region);
    if (opts.key) query.set("key", opts.key);
    for (const tag of opts.tags ?? []) query.append("tag", tag);
    if (opts.stale) query.set("stale", opts.stale);
    if (opts.latency) query.set("latency", opts.latency);
    if (opts.tolerance) query.set("tolerance", opts.tolerance);
    return this.json<Service>("GET", `/v1/lookup/${encodeURIComponent(name)}`, { query });
  }

  /** Every node's view of peer latency, gathered by the connected node. */
  clusterTopology() {
    return this.json<ClusterTopology>("GET", "/v1/topology", { query: new URLSearchParams({ scope: "cluster" }) });
  }

  async credentials(): Promise<Credential[]> {
    return (await this.json<{ credentials: Credential[] }>("GET", "/v1/admin/credentials")).credentials ?? [];
  }

  analytics(days = 30) {
    const query = new URLSearchParams({ days: String(days) });
    return this.json<AdminAnalytics>("GET", "/v1/admin/analytics", { query });
  }

  async backup(): Promise<Blob> {
    return (await this.send("POST", "/v1/admin/backup", { headers: { Accept: "application/vnd.sqlite3" } })).blob();
  }

  evict(nodeId: string) {
    return this.json<{ node_id: string; services_removed: number }>("DELETE", `/v1/admin/nodes/${encodeURIComponent(nodeId)}`);
  }

  /**
   * Works out what the token may do without changing anything: reading the
   * node needs read, the credential listing needs admin, and a PATCH of a
   * service that cannot exist is refused with 403 only without write.
   */
  async detectScopes(): Promise<{ info: NodeInfo; scopes: Scope[] }> {
    const info = await this.node();
    try {
      await this.credentials();
      return { info, scopes: ["read", "write", "admin"] };
    } catch (err) {
      if (!(err instanceof AtlasError) || err.code !== "insufficient_scope") throw err;
    }
    try {
      await this.update("atlas-console-scope-probe-does-not-exist", {});
    } catch (err) {
      if (err instanceof AtlasError && err.code === "insufficient_scope") return { info, scopes: ["read"] };
      if (err instanceof AtlasError && (err.code === "network" || err.code === "unauthorized")) throw err;
    }
    return { info, scopes: ["read", "write"] };
  }

  /**
   * Streams /v1/events until the signal aborts or the stream ends. fetch is
   * used instead of EventSource so the token goes in a header, not the URL.
   */
  async streamEvents(opts: { lastEventId?: string; signal: AbortSignal; onOpen: () => void; onEvent: (event: AtlasEvent, id: string) => void }) {
    const query = new URLSearchParams();
    if (opts.lastEventId) query.set("last_event_id", opts.lastEventId);
    const response = await this.send("GET", "/v1/events", { query, headers: { Accept: "text/event-stream" } }).catch((err) => {
      if (opts.signal.aborted) throw new DOMException("aborted", "AbortError");
      throw err;
    });
    if (!response.body) throw new AtlasError(0, "network", "the event stream has no body");
    opts.onOpen();
    const reader = response.body.getReader();
    const onAbort = () => void reader.cancel().catch(() => {});
    opts.signal.addEventListener("abort", onAbort);
    const decoder = new TextDecoder();
    const parser = new SSEParser();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const frame of parser.feed(decoder.decode(value, { stream: true }))) {
          if (!frame.data) continue;
          try {
            const event = JSON.parse(frame.data) as AtlasEvent;
            if (frame.event === "reset") event.kind = "reset";
            opts.onEvent(event, frame.id ?? event.id ?? "");
          } catch {
            // Ignore frames that are not JSON (none are expected).
          }
        }
      }
    } finally {
      opts.signal.removeEventListener("abort", onAbort);
    }
  }
}

async function toError(response: Response): Promise<AtlasError> {
  let body: { error?: string; code?: string; request_id?: string; owner_node?: string } = {};
  try {
    body = await response.json();
  } catch {
    // Non-JSON error, for example from a proxy.
  }
  const code = body.code ?? (response.status === 401 ? "unauthorized" : response.status === 403 ? "insufficient_scope" : `http_${response.status}`);
  return new AtlasError(response.status, code, body.error ?? response.statusText, body.request_id, body.owner_node);
}
