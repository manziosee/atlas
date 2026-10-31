/**
 * TypeScript client for the Atlas v1 HTTP API. It has no runtime
 * dependencies: it uses the fetch built into Node.js 20+ (and browsers).
 *
 * ```ts
 * const atlas = new AtlasClient("https://atlas.example:8080", process.env.ATLAS_TOKEN!);
 * const me = await atlas.register({ name: "payments", address: "10.0.0.7:8443", ttl_seconds: 15 },
 *   { idempotencyKey: `payments-${hostname()}` });
 * setInterval(() => atlas.heartbeat(me.id), 5_000);
 * const instance = await atlas.lookup("payments", { region: "eu" });
 * ```
 */

export type Health = "unknown" | "healthy" | "unhealthy";

/** A registered service instance. */
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

/**
 * The client-owned part of a registration. Use either `health_check` (an
 * http(s)://, tcp:// or grpc(s):// URL on `address`) or `ttl_seconds` with
 * heartbeats, not both.
 */
export interface ServiceSpec {
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

/** Fields to change; absent fields keep their values. */
export type ServicePatch = Partial<ServiceSpec>;

export interface Member {
  id: string;
  addr: string;
  region: string;
  state: "alive" | "suspect" | "dead" | "left";
  incarnation: number;
  since?: string;
  api_addr?: string;
}

export interface NodeInfo {
  node: Member;
  version: string;
  commit: string;
}

/**
 * A membership event (`member` set: join, update, reap) or registry event
 * (`service` set: service.registered, service.updated, service.health,
 * service.deregistered). `reset` means missed events could not be replayed:
 * re-read current state.
 */
export interface AtlasEvent {
  id?: string;
  type: string;
  kind?: "member" | "service";
  at?: string;
  member?: Member;
  service?: Service;
}

export interface HistoryEntry {
  seq: number;
  service_id: string;
  name: string;
  revision: number;
  change: "registered" | "updated" | "health" | "deregistered";
  at: string;
  /** The record as it was after the change. */
  service: Service;
}

export interface Page<T> {
  items: T[];
  /** Pass to the next call; undefined on the last page. */
  next?: string;
}

export interface ServiceFilter {
  name?: string;
  region?: string;
  health?: Health;
  version?: string;
  node_id?: string;
  /** A service must carry every tag. */
  tags?: string[];
}

export interface LookupOptions {
  region?: string;
  /** Consistent-hash key: the same key keeps selecting the same instance. */
  key?: string;
  tags?: string[];
  /** How to treat instances whose owning node is not alive. */
  stale?: "include" | "prefer-live" | "exclude";
  latency?: "nearest" | "off";
  /** A duration such as "10ms": nodes this close to the nearest count as equal. */
  tolerance?: string;
}

export interface WatchOptions {
  kinds?: Array<"member" | "service">;
  service?: string;
  lastEventId?: string;
  /** Reconnect after drops and resume from the last event (default true). */
  reconnect?: boolean;
  signal?: AbortSignal;
}

export interface ClientOptions {
  /** Per-request timeout in milliseconds (default 5000). Watch streams are exempt. */
  timeoutMs?: number;
  /**
   * A fetch implementation, for example one configured with a private CA.
   * In Node.js you can also trust a CA with NODE_EXTRA_CA_CERTS.
   */
  fetch?: typeof fetch;
}

/**
 * Atlas rejected a request or could not be reached. Branch on `code`
 * (not_found, not_owner, precondition_failed, ...), not on the message.
 * `status` is undefined when the node was unreachable.
 */
export class AtlasError extends Error {
  readonly status?: number;
  readonly code: string;
  readonly requestId: string;
  readonly ownerNode: string;

  constructor(message: string, init: { status?: number; code?: string; requestId?: string; ownerNode?: string } = {}) {
    super(message);
    this.name = "AtlasError";
    this.status = init.status;
    this.code = init.code ?? "";
    this.requestId = init.requestId ?? "";
    this.ownerNode = init.ownerNode ?? "";
  }
}

type Query = Array<[string, string]>;

export class AtlasClient {
  readonly endpoint: string;
  private readonly token: string;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(endpoint: string, token: string, options: ClientOptions = {}) {
    const base = endpoint.replace(/\/+$/, "");
    if (!/^https?:\/\/[^/]+$/.test(base)) {
      throw new TypeError("endpoint must be an http:// or https:// origin, such as https://atlas.example:8080");
    }
    this.endpoint = base;
    this.token = token;
    this.timeoutMs = options.timeoutMs ?? 5000;
    this.fetchImpl = options.fetch ?? globalThis.fetch.bind(globalThis);
  }

  // --- registry ---------------------------------------------------------

  /**
   * Register an instance owned by the receiving node. With `serviceId` or
   * `idempotencyKey`, repeating the call returns the same record instead of
   * creating a duplicate.
   */
  async register(spec: ServiceSpec, options: { serviceId?: string; idempotencyKey?: string } = {}): Promise<Service> {
    const headers: Record<string, string> = {};
    if (options.idempotencyKey) headers["Idempotency-Key"] = options.idempotencyKey;
    const body = options.serviceId ? { id: options.serviceId, ...spec } : spec;
    return this.request<Service>("POST", "/v1/services", [], body, headers);
  }

  /** Create or fully replace the service with `id` (PUT). */
  async replace(id: string, spec: ServiceSpec, ifRevision = 0): Promise<Service> {
    return this.request<Service>("PUT", servicePath(id), [], spec, ifMatch(ifRevision));
  }

  /**
   * Change only the given fields (PATCH). Any node accepts this; a node that
   * does not own the service forwards it to the owner. With `ifRevision`,
   * the call fails with code precondition_failed if the service changed.
   */
  async update(id: string, patch: ServicePatch, ifRevision = 0): Promise<Service> {
    return this.request<Service>("PATCH", servicePath(id), [], patch, ifMatch(ifRevision));
  }

  /** Stop new traffic to an instance while it stays registered. */
  async drain(id: string): Promise<Service> {
    return this.update(id, { draining: true });
  }

  /** Return a drained instance to rotation. */
  async undrain(id: string): Promise<Service> {
    return this.update(id, { draining: false });
  }

  /** Refresh a TTL service. A not_found error means it expired: register again. */
  async heartbeat(id: string): Promise<Service> {
    return this.request<Service>("POST", `${servicePath(id)}/heartbeat`);
  }

  async get(id: string): Promise<Service> {
    return this.request<Service>("GET", servicePath(id));
  }

  async delete(id: string): Promise<void> {
    await this.request<void>("DELETE", servicePath(id));
  }

  /** One page of services; pass `next` from the previous page as `cursor`. */
  async listPage(filter: ServiceFilter = {}, cursor = "", limit = 0): Promise<Page<Service>> {
    const query = filterQuery(filter);
    if (cursor) query.push(["cursor", cursor]);
    if (limit) query.push(["limit", String(limit)]);
    const body = await this.request<{ services: Service[]; next_cursor?: string }>("GET", "/v1/services", query);
    return { items: body.services ?? [], next: body.next_cursor || undefined };
  }

  /** Every matching service, fetching pages as needed. */
  async *iterServices(filter: ServiceFilter = {}): AsyncGenerator<Service> {
    let cursor = "";
    do {
      const page = await this.listPage(filter, cursor, 1000);
      yield* page.items;
      cursor = page.next ?? "";
    } while (cursor);
  }

  /** Every matching service as an array. */
  async listServices(filter: ServiceFilter = {}): Promise<Service[]> {
    const all: Service[] = [];
    for await (const service of this.iterServices(filter)) all.push(service);
    return all;
  }

  /** Select one routable instance using the node's routing rules. */
  async lookup(name: string, options: LookupOptions = {}): Promise<Service> {
    const query: Query = [];
    for (const key of ["region", "key", "stale", "latency", "tolerance"] as const) {
      const value = options[key];
      if (value) query.push([key, value]);
    }
    for (const tag of options.tags ?? []) query.push(["tag", tag]);
    return this.request<Service>("GET", `/v1/lookup/${encodeURIComponent(name)}`, query);
  }

  /** Recorded changes to one service, newest first, including after deletion. */
  async history(id: string, options: { before?: number; limit?: number } = {}): Promise<Page<HistoryEntry>> {
    return this.historyPage(`${servicePath(id)}/history`, [], options);
  }

  /** Recent changes across services, or for every service called `name`. */
  async recentHistory(options: { name?: string; before?: number; limit?: number } = {}): Promise<Page<HistoryEntry>> {
    return this.historyPage("/v1/history", options.name ? [["name", options.name]] : [], options);
  }

  // --- cluster ------------------------------------------------------------

  async node(): Promise<NodeInfo> {
    return this.request<NodeInfo>("GET", "/v1/node");
  }

  async members(): Promise<Member[]> {
    return (await this.request<{ members: Member[] }>("GET", "/v1/members")).members;
  }

  /**
   * Stream membership and registry events. The iterator reconnects after a
   * dropped connection and resumes from the last event it yielded; break out
   * of the loop or abort `signal` to stop.
   */
  async *watch(options: WatchOptions = {}): AsyncGenerator<AtlasEvent> {
    const reconnect = options.reconnect ?? true;
    let lastEventId = options.lastEventId ?? "";
    let backoff = 500;
    for (;;) {
      let delivered = false;
      try {
        for await (const event of this.streamOnce(options, lastEventId)) {
          delivered = true;
          if (event.id) lastEventId = event.id;
          yield event;
        }
      } catch (error) {
        if (options.signal?.aborted) return;
        if (!reconnect || (error instanceof AtlasError && error.status !== undefined)) throw error;
      }
      if (!reconnect || options.signal?.aborted) return;
      backoff = delivered ? 500 : Math.min(backoff * 2, 30_000);
      await sleep(backoff, options.signal);
      if (options.signal?.aborted) return;
    }
  }

  // --- admin (admin scope) ---------------------------------------------

  async credentials(): Promise<Array<Record<string, unknown>>> {
    return (await this.request<{ credentials: Array<Record<string, unknown>> }>("GET", "/v1/admin/credentials")).credentials;
  }

  /** A consistent SQLite snapshot of the node's registry. */
  async backup(): Promise<Uint8Array> {
    const response = await this.send("POST", "/v1/admin/backup", [], undefined, {}, 300_000);
    return new Uint8Array(await response.arrayBuffer());
  }

  async evictNode(nodeId: string): Promise<{ node_id: string; services_removed: number }> {
    return this.request("DELETE", `/v1/admin/nodes/${encodeURIComponent(nodeId)}`);
  }

  // --- transport ----------------------------------------------------------

  private async historyPage(path: string, query: Query, options: { before?: number; limit?: number }): Promise<Page<HistoryEntry>> {
    if (options.before) query.push(["before", String(options.before)]);
    if (options.limit) query.push(["limit", String(options.limit)]);
    const body = await this.request<{ entries: HistoryEntry[]; next_before?: number }>("GET", path, query);
    return { items: body.entries ?? [], next: body.next_before ? String(body.next_before) : undefined };
  }

  private async request<T>(method: string, path: string, query: Query = [], body?: unknown, headers: Record<string, string> = {}): Promise<T> {
    const response = await this.send(method, path, query, body, headers, this.timeoutMs);
    if (response.status === 204) return undefined as T;
    try {
      return (await response.json()) as T;
    } catch (error) {
      throw new AtlasError(`Atlas returned an unreadable response: ${String(error)}`, { status: response.status });
    }
  }

  private async send(method: string, path: string, query: Query, body: unknown, headers: Record<string, string>, timeoutMs: number | null, signal?: AbortSignal): Promise<Response> {
    const url = this.endpoint + path + (query.length ? `?${new URLSearchParams(query).toString()}` : "");
    const signals = [signal, timeoutMs === null ? undefined : AbortSignal.timeout(timeoutMs)].filter((s): s is AbortSignal => s !== undefined);
    let response: Response;
    try {
      response = await this.fetchImpl(url, {
        method,
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${this.token}`,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
          ...headers,
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: signals.length ? AbortSignal.any(signals) : undefined,
      });
    } catch (error) {
      throw new AtlasError(`Atlas request failed: ${error instanceof Error ? error.message : String(error)}`);
    }
    if (!response.ok) throw await errorFrom(response);
    return response;
  }

  private async *streamOnce(options: WatchOptions, lastEventId: string): AsyncGenerator<AtlasEvent> {
    const query: Query = [];
    if (options.kinds?.length) query.push(["kinds", options.kinds.join(",")]);
    if (options.service) query.push(["service", options.service]);
    const headers: Record<string, string> = { Accept: "text/event-stream" };
    if (lastEventId) headers["Last-Event-ID"] = lastEventId;
    const response = await this.send("GET", "/v1/events", query, undefined, headers, null, options.signal);
    if (!response.body) throw new AtlasError("Atlas event stream has no body");
    yield* parseEvents(response.body);
  }
}

/** Parse a Server-Sent Events byte stream into Atlas events. */
export async function* parseEvents(body: ReadableStream<Uint8Array>): AsyncGenerator<AtlasEvent> {
  const decoder = new TextDecoder();
  let buffer = "";
  let eventType = "";
  for await (const chunk of body as unknown as AsyncIterable<Uint8Array>) {
    buffer += decoder.decode(chunk, { stream: true });
    let newline: number;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, newline).replace(/\r$/, "");
      buffer = buffer.slice(newline + 1);
      if (line.startsWith("event: ")) {
        eventType = line.slice(7);
      } else if (line.startsWith("data: ")) {
        const event = JSON.parse(line.slice(6)) as AtlasEvent;
        event.type ||= eventType;
        yield event;
      } else if (line === "") {
        eventType = "";
      }
    }
  }
}

function servicePath(id: string): string {
  return `/v1/services/${encodeURIComponent(id)}`;
}

function ifMatch(revision: number): Record<string, string> {
  return revision ? { "If-Match": `"${revision}"` } : {};
}

function filterQuery(filter: ServiceFilter): Query {
  const query: Query = [];
  for (const key of ["name", "region", "health", "version", "node_id"] as const) {
    const value = filter[key];
    if (value) query.push([key, value]);
  }
  for (const tag of filter.tags ?? []) query.push(["tag", tag]);
  return query;
}

async function errorFrom(response: Response): Promise<AtlasError> {
  const text = await response.text().catch(() => "");
  try {
    const body = JSON.parse(text) as { error?: string; code?: string; request_id?: string; owner_node?: string };
    if (body.error) {
      return new AtlasError(body.error, { status: response.status, code: body.code, requestId: body.request_id, ownerNode: body.owner_node });
    }
  } catch {
    // Not a JSON error body; fall through to the status line.
  }
  return new AtlasError(text.trim() || response.statusText, { status: response.status });
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, { once: true });
  });
}
