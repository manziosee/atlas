package io.github.manziosee.atlas;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** Data types returned and accepted by {@link AtlasClient}. */
public final class Types {
  private Types() {}

  /** A registered service instance. Times are RFC 3339 strings; absent fields are empty or zero. */
  public record Service(
      String id,
      String name,
      String nodeId,
      String address,
      String region,
      String version,
      List<String> tags,
      String healthCheck,
      String health,
      long revision,
      long ttlSeconds,
      long checkIntervalSeconds,
      long unhealthyThreshold,
      long healthyThreshold,
      boolean draining,
      String updatedAt,
      String checkedAt,
      String heartbeatAt) {

    static Service from(Map<String, Object> m) {
      return new Service(
          str(m, "id"), str(m, "name"), str(m, "node_id"), str(m, "address"), str(m, "region"),
          str(m, "version"), strings(m, "tags"), str(m, "health_check"), str(m, "health"),
          num(m, "revision"), num(m, "ttl_seconds"), num(m, "check_interval_seconds"),
          num(m, "unhealthy_threshold"), num(m, "healthy_threshold"), bool(m, "draining"),
          str(m, "updated_at"), str(m, "checked_at"), str(m, "heartbeat_at"));
    }
  }

  /** One cluster member as seen by the answering node. State is alive, suspect, dead or left. */
  public record Member(String id, String addr, String region, String state, long incarnation, String since, String apiAddr) {
    static Member from(Map<String, Object> m) {
      return new Member(str(m, "id"), str(m, "addr"), str(m, "region"), str(m, "state"),
          num(m, "incarnation"), str(m, "since"), str(m, "api_addr"));
    }
  }

  /** The answering node, with its build version and commit. */
  public record NodeInfo(Member node, String version, String commit) {}

  /**
   * A membership event ({@code member} set: join, update, reap) or registry
   * event ({@code service} set: service.registered, service.updated,
   * service.health, service.deregistered). {@code reset} means missed events
   * could not be replayed: re-read current state.
   */
  public record Event(String id, String type, String kind, String at, Member member, Service service) {
    static Event from(Map<String, Object> m, String fallbackType) {
      String type = str(m, "type");
      return new Event(str(m, "id"), type.isEmpty() ? fallbackType : type, str(m, "kind"), str(m, "at"),
          obj(m, "member") == null ? null : Member.from(obj(m, "member")),
          obj(m, "service") == null ? null : Service.from(obj(m, "service")));
    }
  }

  /** One recorded change; {@code service} is the record after it. */
  public record HistoryEntry(long seq, String serviceId, String name, long revision, String change, String at, Service service) {
    static HistoryEntry from(Map<String, Object> m) {
      Map<String, Object> service = obj(m, "service");
      return new HistoryEntry(num(m, "seq"), str(m, "service_id"), str(m, "name"), num(m, "revision"),
          str(m, "change"), str(m, "at"), service == null ? null : Service.from(service));
    }
  }

  /** One page of results; {@code next} is null on the last page. */
  public record Page<T>(List<T> items, String next) {}

  /**
   * The client-owned part of a registration. Use either {@code healthCheck}
   * (an http(s)://, tcp:// or grpc(s):// URL on the address) or
   * {@code ttlSeconds} with heartbeats, not both.
   */
  public static final class ServiceSpec {
    final Map<String, Object> fields = new LinkedHashMap<>();

    public ServiceSpec(String name, String address) {
      fields.put("name", name);
      fields.put("address", address);
    }

    public ServiceSpec version(String version) { fields.put("version", version); return this; }
    public ServiceSpec tags(List<String> tags) { fields.put("tags", new ArrayList<>(tags)); return this; }
    public ServiceSpec healthCheck(String url) { fields.put("health_check", url); return this; }
    public ServiceSpec ttlSeconds(long seconds) { fields.put("ttl_seconds", seconds); return this; }
    public ServiceSpec checkIntervalSeconds(long seconds) { fields.put("check_interval_seconds", seconds); return this; }
    public ServiceSpec unhealthyThreshold(long checks) { fields.put("unhealthy_threshold", checks); return this; }
    public ServiceSpec healthyThreshold(long checks) { fields.put("healthy_threshold", checks); return this; }
    public ServiceSpec draining(boolean draining) { fields.put("draining", draining); return this; }
  }

  /** Fields to change with {@link AtlasClient#update}; unset fields keep their values. */
  public static final class ServicePatch {
    final Map<String, Object> fields = new LinkedHashMap<>();

    public ServicePatch name(String name) { fields.put("name", name); return this; }
    public ServicePatch address(String address) { fields.put("address", address); return this; }
    public ServicePatch version(String version) { fields.put("version", version); return this; }
    public ServicePatch tags(List<String> tags) { fields.put("tags", new ArrayList<>(tags)); return this; }
    public ServicePatch healthCheck(String url) { fields.put("health_check", url); return this; }
    public ServicePatch ttlSeconds(long seconds) { fields.put("ttl_seconds", seconds); return this; }
    public ServicePatch checkIntervalSeconds(long seconds) { fields.put("check_interval_seconds", seconds); return this; }
    public ServicePatch unhealthyThreshold(long checks) { fields.put("unhealthy_threshold", checks); return this; }
    public ServicePatch healthyThreshold(long checks) { fields.put("healthy_threshold", checks); return this; }
    public ServicePatch draining(boolean draining) { fields.put("draining", draining); return this; }
  }

  /** Narrows listings. A service must carry every tag. */
  public static final class Filter {
    final Map<String, String> fields = new LinkedHashMap<>();
    final List<String> tags = new ArrayList<>();

    public Filter name(String name) { fields.put("name", name); return this; }
    public Filter region(String region) { fields.put("region", region); return this; }
    public Filter health(String health) { fields.put("health", health); return this; }
    public Filter version(String version) { fields.put("version", version); return this; }
    public Filter nodeId(String nodeId) { fields.put("node_id", nodeId); return this; }
    public Filter tag(String tag) { tags.add(tag); return this; }
  }

  /**
   * Refines a routed lookup. {@code stale} (include, prefer-live, exclude),
   * {@code latency} (nearest, off) and {@code tolerance} (a duration such as
   * "10ms") override the node's routing policy for this request.
   */
  public static final class LookupOptions {
    final Map<String, String> fields = new LinkedHashMap<>();
    final List<String> tags = new ArrayList<>();

    public LookupOptions region(String region) { fields.put("region", region); return this; }
    public LookupOptions key(String key) { fields.put("key", key); return this; }
    public LookupOptions tag(String tag) { tags.add(tag); return this; }
    public LookupOptions stale(String policy) { fields.put("stale", policy); return this; }
    public LookupOptions latency(String mode) { fields.put("latency", mode); return this; }
    public LookupOptions tolerance(String duration) { fields.put("tolerance", duration); return this; }
  }

  static String str(Map<String, Object> m, String key) {
    Object value = m.get(key);
    return value == null ? "" : value.toString();
  }

  static long num(Map<String, Object> m, String key) {
    return m.get(key) instanceof Number n ? n.longValue() : 0;
  }

  static boolean bool(Map<String, Object> m, String key) {
    return Boolean.TRUE.equals(m.get(key));
  }

  @SuppressWarnings("unchecked")
  static Map<String, Object> obj(Map<String, Object> m, String key) {
    return m.get(key) instanceof Map<?, ?> map ? (Map<String, Object>) map : null;
  }

  @SuppressWarnings("unchecked")
  static List<Map<String, Object>> objects(Map<String, Object> m, String key) {
    List<Map<String, Object>> out = new ArrayList<>();
    if (m.get(key) instanceof List<?> list) {
      for (Object item : list) {
        if (item instanceof Map<?, ?> map) out.add((Map<String, Object>) map);
      }
    }
    return out;
  }

  static List<String> strings(Map<String, Object> m, String key) {
    List<String> out = new ArrayList<>();
    if (m.get(key) instanceof List<?> list) {
      for (Object item : list) out.add(String.valueOf(item));
    }
    return List.copyOf(out);
  }
}
