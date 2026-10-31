package io.github.manziosee.atlas;

import io.github.manziosee.atlas.Types.Event;
import io.github.manziosee.atlas.Types.Filter;
import io.github.manziosee.atlas.Types.HistoryEntry;
import io.github.manziosee.atlas.Types.LookupOptions;
import io.github.manziosee.atlas.Types.Member;
import io.github.manziosee.atlas.Types.NodeInfo;
import io.github.manziosee.atlas.Types.Page;
import io.github.manziosee.atlas.Types.Service;
import io.github.manziosee.atlas.Types.ServicePatch;
import io.github.manziosee.atlas.Types.ServiceSpec;
import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Predicate;

/**
 * Client for the Atlas v1 HTTP API, built on {@code java.net.http} with no
 * runtime dependencies.
 *
 * <pre>{@code
 * AtlasClient atlas = new AtlasClient("https://atlas.example:8080", System.getenv("ATLAS_TOKEN"));
 * Service me = atlas.register(new ServiceSpec("payments", "10.0.0.7:8443").ttlSeconds(15), null, "payments-host-7");
 * atlas.heartbeat(me.id());
 * Service instance = atlas.lookup("payments", new LookupOptions().region("eu"));
 * }</pre>
 *
 * <p>For a private CA or mutual TLS, pass an {@link HttpClient} configured
 * with your {@code SSLContext}.
 */
public final class AtlasClient {
  private final String endpoint;
  private final String token;
  private final HttpClient http;
  private final Duration timeout;

  public AtlasClient(String endpoint, String token) {
    this(endpoint, token, HttpClient.newHttpClient(), Duration.ofSeconds(5));
  }

  /** {@code timeout} bounds each request; watch streams are exempt. */
  public AtlasClient(String endpoint, String token, HttpClient http, Duration timeout) {
    String base = endpoint.replaceAll("/+$", "");
    if (!base.matches("https?://[^/]+")) {
      throw new IllegalArgumentException("endpoint must be an http:// or https:// origin, such as https://atlas.example:8080");
    }
    this.endpoint = base;
    this.token = token;
    this.http = http;
    this.timeout = timeout;
  }

  // --- registry ---------------------------------------------------------

  /**
   * Register an instance owned by the receiving node. With a
   * {@code serviceId} or {@code idempotencyKey} (either may be null),
   * repeating the call returns the same record instead of a duplicate.
   */
  public Service register(ServiceSpec spec, String serviceId, String idempotencyKey) {
    Map<String, Object> body = new LinkedHashMap<>();
    if (serviceId != null && !serviceId.isEmpty()) body.put("id", serviceId);
    body.putAll(spec.fields);
    Map<String, String> headers = new LinkedHashMap<>();
    if (idempotencyKey != null && !idempotencyKey.isEmpty()) headers.put("Idempotency-Key", idempotencyKey);
    return Service.from(object("POST", "/v1/services", List.of(), body, headers));
  }

  public Service register(ServiceSpec spec) {
    return register(spec, null, null);
  }

  /** Create or fully replace the service with {@code id} (PUT); 0 skips the revision check. */
  public Service replace(String id, ServiceSpec spec, long ifRevision) {
    return Service.from(object("PUT", servicePath(id), List.of(), spec.fields, ifMatch(ifRevision)));
  }

  /**
   * Change only the fields set in {@code patch}. Any node accepts this and
   * forwards it to the owner. With a non-zero {@code ifRevision}, the call
   * fails with code precondition_failed if the service changed.
   */
  public Service update(String id, ServicePatch patch, long ifRevision) {
    return Service.from(object("PATCH", servicePath(id), List.of(), patch.fields, ifMatch(ifRevision)));
  }

  /** Stop new traffic to an instance while it stays registered. */
  public Service drain(String id) {
    return update(id, new ServicePatch().draining(true), 0);
  }

  /** Return a drained instance to rotation. */
  public Service undrain(String id) {
    return update(id, new ServicePatch().draining(false), 0);
  }

  /** Refresh a TTL service. Code not_found means it expired: register again. */
  public Service heartbeat(String id) {
    return Service.from(object("POST", servicePath(id) + "/heartbeat", List.of(), null, Map.of()));
  }

  public Service get(String id) {
    return Service.from(object("GET", servicePath(id), List.of(), null, Map.of()));
  }

  public void delete(String id) {
    send("DELETE", servicePath(id), List.of(), null, Map.of());
  }

  /** One page of services; pass the previous page's {@code next} as cursor (or null). */
  public Page<Service> listPage(Filter filter, String cursor, int limit) {
    List<String[]> query = new ArrayList<>();
    filter.fields.forEach((k, v) -> query.add(new String[] {k, v}));
    filter.tags.forEach(tag -> query.add(new String[] {"tag", tag}));
    if (cursor != null && !cursor.isEmpty()) query.add(new String[] {"cursor", cursor});
    if (limit > 0) query.add(new String[] {"limit", String.valueOf(limit)});
    Map<String, Object> body = object("GET", "/v1/services", query, null, Map.of());
    List<Service> items = new ArrayList<>();
    for (Map<String, Object> m : Types.objects(body, "services")) items.add(Service.from(m));
    String next = Types.str(body, "next_cursor");
    return new Page<>(items, next.isEmpty() ? null : next);
  }

  /** Every matching service, following pages. */
  public List<Service> listServices(Filter filter) {
    List<Service> all = new ArrayList<>();
    String cursor = null;
    do {
      Page<Service> page = listPage(filter, cursor, 1000);
      all.addAll(page.items());
      cursor = page.next();
    } while (cursor != null);
    return all;
  }

  /** Select one routable instance using the node's routing rules. */
  public Service lookup(String name, LookupOptions options) {
    List<String[]> query = new ArrayList<>();
    options.fields.forEach((k, v) -> query.add(new String[] {k, v}));
    options.tags.forEach(tag -> query.add(new String[] {"tag", tag}));
    return Service.from(object("GET", "/v1/lookup/" + encode(name), query, null, Map.of()));
  }

  public Service lookup(String name) {
    return lookup(name, new LookupOptions());
  }

  /** Recorded changes to one service, newest first; before 0 starts at the newest. */
  public Page<HistoryEntry> history(String id, long before, int limit) {
    return historyPage(servicePath(id) + "/history", new ArrayList<>(), before, limit);
  }

  /** Recent changes across services, or for every service called {@code name} (may be null). */
  public Page<HistoryEntry> recentHistory(String name, long before, int limit) {
    List<String[]> query = new ArrayList<>();
    if (name != null && !name.isEmpty()) query.add(new String[] {"name", name});
    return historyPage("/v1/history", query, before, limit);
  }

  // --- cluster ------------------------------------------------------------

  public NodeInfo node() {
    Map<String, Object> body = object("GET", "/v1/node", List.of(), null, Map.of());
    return new NodeInfo(Member.from(Types.obj(body, "node")), Types.str(body, "version"), Types.str(body, "commit"));
  }

  public List<Member> members() {
    List<Member> out = new ArrayList<>();
    for (Map<String, Object> m : Types.objects(object("GET", "/v1/members", List.of(), null, Map.of()), "members")) {
      out.add(Member.from(m));
    }
    return out;
  }

  /**
   * Stream membership and registry events to {@code handler} on the calling
   * thread until it returns false or the thread is interrupted. With
   * {@code reconnect}, the stream resumes after drops from the last event.
   *
   * @param kinds "member", "service" or null for both
   * @param service only service events for this name, or null
   * @param lastEventId resume after this event, or null
   */
  public void watch(String kinds, String service, String lastEventId, boolean reconnect, Predicate<Event> handler) {
    String last = lastEventId == null ? "" : lastEventId;
    long backoff = 500;
    while (!Thread.currentThread().isInterrupted()) {
      boolean delivered = false;
      try {
        List<String[]> query = new ArrayList<>();
        if (kinds != null && !kinds.isEmpty()) query.add(new String[] {"kinds", kinds});
        if (service != null && !service.isEmpty()) query.add(new String[] {"service", service});
        HttpRequest.Builder request = builder("GET", "/v1/events", query, null).header("Accept", "text/event-stream");
        if (!last.isEmpty()) request.header("Last-Event-ID", last);
        HttpResponse<InputStream> response = http.send(request.build(), HttpResponse.BodyHandlers.ofInputStream());
        if (response.statusCode() != 200) {
          try (InputStream body = response.body()) {
            throw errorFrom(response.statusCode(), new String(body.readAllBytes(), StandardCharsets.UTF_8));
          }
        }
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(response.body(), StandardCharsets.UTF_8))) {
          String line;
          String eventType = "";
          while ((line = reader.readLine()) != null) {
            if (line.startsWith("event: ")) {
              eventType = line.substring(7);
            } else if (line.startsWith("data: ")) {
              Event event = Event.from(Json.parseObject(line.substring(6)), eventType);
              delivered = true;
              if (!event.id().isEmpty()) last = event.id();
              if (!handler.test(event)) return;
            } else if (line.isEmpty()) {
              eventType = "";
            }
          }
        }
      } catch (AtlasException e) {
        if (!reconnect || e.status() != null) throw e;
      } catch (IOException e) {
        if (!reconnect) throw new AtlasException("Atlas event stream failed: " + e.getMessage(), null, "", "", "", e);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
        return;
      }
      if (!reconnect) return;
      backoff = delivered ? 500 : Math.min(backoff * 2, 30_000);
      try {
        Thread.sleep(backoff);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
        return;
      }
    }
  }

  // --- admin (admin scope) ---------------------------------------------

  public List<Map<String, Object>> credentials() {
    return Types.objects(object("GET", "/v1/admin/credentials", List.of(), null, Map.of()), "credentials");
  }

  /** A consistent SQLite snapshot of the node's registry. */
  public byte[] backup() {
    return send("POST", "/v1/admin/backup", List.of(), null, Map.of(), Duration.ofMinutes(5)).body();
  }

  /** Evict a failed node and delete its services; returns how many were removed. */
  public long evictNode(String nodeId) {
    return Types.num(object("DELETE", "/v1/admin/nodes/" + encode(nodeId), List.of(), null, Map.of()), "services_removed");
  }

  // --- transport ----------------------------------------------------------

  private Page<HistoryEntry> historyPage(String path, List<String[]> query, long before, int limit) {
    if (before > 0) query.add(new String[] {"before", String.valueOf(before)});
    if (limit > 0) query.add(new String[] {"limit", String.valueOf(limit)});
    Map<String, Object> body = object("GET", path, query, null, Map.of());
    List<HistoryEntry> items = new ArrayList<>();
    for (Map<String, Object> m : Types.objects(body, "entries")) items.add(HistoryEntry.from(m));
    long next = Types.num(body, "next_before");
    return new Page<>(items, next == 0 ? null : String.valueOf(next));
  }

  private Map<String, Object> object(String method, String path, List<String[]> query, Object body, Map<String, String> headers) {
    HttpResponse<byte[]> response = send(method, path, query, body, headers);
    String text = new String(response.body(), StandardCharsets.UTF_8);
    try {
      return Json.parseObject(text);
    } catch (IllegalArgumentException e) {
      throw new AtlasException("Atlas returned an unreadable response: " + e.getMessage(), response.statusCode(), "", "", "", e);
    }
  }

  private HttpResponse<byte[]> send(String method, String path, List<String[]> query, Object body, Map<String, String> headers) {
    return send(method, path, query, body, headers, timeout);
  }

  private HttpResponse<byte[]> send(String method, String path, List<String[]> query, Object body, Map<String, String> headers, Duration limit) {
    HttpRequest.Builder request = builder(method, path, query, body).timeout(limit).header("Accept", "application/json");
    headers.forEach(request::header);
    HttpResponse<byte[]> response;
    try {
      response = http.send(request.build(), HttpResponse.BodyHandlers.ofByteArray());
    } catch (IOException e) {
      throw new AtlasException("Atlas request failed: " + e.getMessage(), null, "", "", "", e);
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
      throw new AtlasException("Atlas request interrupted", null, "", "", "", e);
    }
    if (response.statusCode() < 200 || response.statusCode() >= 300) {
      throw errorFrom(response.statusCode(), new String(response.body(), StandardCharsets.UTF_8));
    }
    return response;
  }

  private HttpRequest.Builder builder(String method, String path, List<String[]> query, Object body) {
    StringBuilder url = new StringBuilder(endpoint).append(path);
    for (int i = 0; i < query.size(); i++) {
      url.append(i == 0 ? '?' : '&').append(encode(query.get(i)[0])).append('=').append(encode(query.get(i)[1]));
    }
    HttpRequest.Builder request = HttpRequest.newBuilder(URI.create(url.toString())).header("Authorization", "Bearer " + token);
    if (body == null) {
      return request.method(method, HttpRequest.BodyPublishers.noBody());
    }
    return request.header("Content-Type", "application/json")
        .method(method, HttpRequest.BodyPublishers.ofString(Json.write(body), StandardCharsets.UTF_8));
  }

  private static AtlasException errorFrom(int status, String text) {
    try {
      Map<String, Object> body = Json.parseObject(text);
      String message = Types.str(body, "error");
      if (!message.isEmpty()) {
        return new AtlasException(message, status, Types.str(body, "code"), Types.str(body, "request_id"), Types.str(body, "owner_node"), null);
      }
    } catch (IllegalArgumentException ignored) {
      // Not a JSON error body; use the raw text below.
    }
    return new AtlasException(text.isBlank() ? "HTTP " + status : text.strip(), status, "", "", "", null);
  }

  private static String servicePath(String id) {
    return "/v1/services/" + encode(id);
  }

  private static Map<String, String> ifMatch(long revision) {
    return revision > 0 ? Map.of("If-Match", "\"" + revision + "\"") : Map.of();
  }

  private static String encode(String value) {
    return URLEncoder.encode(value, StandardCharsets.UTF_8).replace("+", "%20");
  }
}
