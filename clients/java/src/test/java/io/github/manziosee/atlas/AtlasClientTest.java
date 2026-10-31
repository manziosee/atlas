package io.github.manziosee.atlas;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

import io.github.manziosee.atlas.Types.Event;
import io.github.manziosee.atlas.Types.Filter;
import io.github.manziosee.atlas.Types.HistoryEntry;
import io.github.manziosee.atlas.Types.LookupOptions;
import io.github.manziosee.atlas.Types.Page;
import io.github.manziosee.atlas.Types.Service;
import io.github.manziosee.atlas.Types.ServicePatch;
import io.github.manziosee.atlas.Types.ServiceSpec;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * Integration tests against a running Atlas node. Set ATLAS_TEST_ENDPOINT and
 * an admin ATLAS_TEST_TOKEN to run them; CI starts a node for this. The
 * offline tests always run.
 */
class AtlasClientTest {
  private static final String ENDPOINT = System.getenv().getOrDefault("ATLAS_TEST_ENDPOINT", "");
  private static final String TOKEN = System.getenv().getOrDefault("ATLAS_TEST_TOKEN", "");

  private AtlasClient atlas;

  @BeforeEach
  void connect() {
    if (!ENDPOINT.isEmpty() && !TOKEN.isEmpty()) atlas = new AtlasClient(ENDPOINT, TOKEN);
  }

  private void live() {
    assumeTrue(atlas != null, "set ATLAS_TEST_ENDPOINT and ATLAS_TEST_TOKEN");
  }

  private static String unique(String prefix) {
    return prefix + "-" + UUID.randomUUID().toString().substring(0, 8);
  }

  @Test
  void nodeAndMembers() {
    live();
    String id = atlas.node().node().id();
    assertFalse(id.isEmpty());
    assertTrue(atlas.members().stream().anyMatch(m -> m.id().equals(id)));
  }

  @Test
  void idempotentRegistrationUpdateAndDelete() {
    live();
    String name = unique("java");
    ServiceSpec spec = new ServiceSpec(name, "10.7.0.1:80").tags(List.of("java"));
    Service first = atlas.register(spec, null, "deploy-" + name);
    Service again = atlas.register(spec, null, "deploy-" + name);
    assertEquals(first.id(), again.id());
    assertEquals(1, again.revision());

    Service updated = atlas.update(first.id(), new ServicePatch().version("2"), 1);
    assertEquals("2", updated.version());
    assertEquals("10.7.0.1:80", updated.address());
    AtlasException stale = assertThrows(AtlasException.class, () -> atlas.update(first.id(), new ServicePatch().version("3"), 1));
    assertEquals("precondition_failed", stale.code());
    assertEquals(412, stale.status());

    Service replaced = atlas.replace(first.id(), new ServiceSpec(name, "10.7.0.2:80"), updated.revision());
    assertEquals("10.7.0.2:80", replaced.address());
    assertTrue(replaced.tags().isEmpty());
    assertEquals(first.id(), atlas.lookup(name).id());

    atlas.delete(first.id());
    AtlasException missing = assertThrows(AtlasException.class, () -> atlas.get(first.id()));
    assertEquals("not_found", missing.code());
    assertFalse(missing.requestId().isEmpty());
  }

  @Test
  void ttlHeartbeats() {
    live();
    Service service = atlas.register(new ServiceSpec(unique("ttl"), "10.7.1.1:80").ttlSeconds(30));
    assertEquals("unknown", service.health());
    assertEquals("healthy", atlas.heartbeat(service.id()).health());
    Service plain = atlas.register(new ServiceSpec(unique("plain"), "10.7.1.2:80"));
    assertEquals("no_ttl", assertThrows(AtlasException.class, () -> atlas.heartbeat(plain.id())).code());
  }

  @Test
  void pagingAndFilters() {
    live();
    String name = unique("page");
    for (int i = 0; i < 5; i++) {
      atlas.register(new ServiceSpec(name, "10.7.2." + (i + 1) + ":80").tags(List.of(i % 2 == 0 ? "even" : "odd")));
    }
    Page<Service> page = atlas.listPage(new Filter().name(name), null, 2);
    assertEquals(2, page.items().size());
    assertNotNull(page.next());
    assertEquals(5, atlas.listServices(new Filter().name(name)).size());
    assertEquals(3, atlas.listServices(new Filter().name(name).tag("even")).size());
  }

  @Test
  void drainingAndRoutingOverrides() {
    live();
    String name = unique("drain");
    Service blue = atlas.register(new ServiceSpec(name, "10.7.3.1:80"));
    Service green = atlas.register(new ServiceSpec(name, "10.7.3.2:80").healthCheck("tcp://10.7.3.2:80").unhealthyThreshold(4));
    assertEquals(4, green.unhealthyThreshold());
    assertTrue(atlas.drain(blue.id()).draining());
    for (int i = 0; i < 4; i++) {
      assertFalse(atlas.lookup(name, new LookupOptions().stale("exclude").latency("off")).id().equals(blue.id()));
    }
    assertFalse(atlas.undrain(blue.id()).draining());
    AtlasException bad = assertThrows(AtlasException.class, () -> atlas.lookup(name, new LookupOptions().stale("sometimes")));
    assertEquals("validation_failed", bad.code());
  }

  @Test
  void serviceHistory() {
    live();
    String name = unique("hist");
    Service service = atlas.register(new ServiceSpec(name, "10.7.4.1:80"));
    atlas.update(service.id(), new ServicePatch().version("2"), 0);
    Page<HistoryEntry> first = atlas.history(service.id(), 0, 1);
    assertEquals("updated", first.items().get(0).change());
    assertNotNull(first.next());
    Page<HistoryEntry> rest = atlas.history(service.id(), Long.parseLong(first.next()), 0);
    assertEquals(List.of("registered"), rest.items().stream().map(HistoryEntry::change).toList());
    assertNull(rest.next());
    assertEquals(2, atlas.recentHistory(name, 0, 0).items().size());
  }

  @Test
  void watchStreamsServiceEventsAndResets() throws Exception {
    live();
    String name = unique("watch");
    AtomicReference<Event> received = new AtomicReference<>();
    Thread watcher = new Thread(() -> atlas.watch("service", name, null, true, event -> {
      received.set(event);
      return false;
    }));
    watcher.start();
    Thread.sleep(500);
    atlas.register(new ServiceSpec(name, "10.7.5.1:80"));
    watcher.join(10_000);
    assertFalse(watcher.isAlive(), "watcher did not receive the registration");
    assertEquals("service.registered", received.get().type());
    assertEquals(name, received.get().service().name());
    assertFalse(received.get().id().isEmpty());

    AtomicReference<String> reset = new AtomicReference<>();
    atlas.watch(null, null, "elsewhere.1", false, event -> {
      reset.set(event.type());
      return false;
    });
    assertEquals("reset", reset.get());
  }

  @Test
  void adminOperations() {
    live();
    assertTrue(atlas.credentials().stream().anyMatch(c -> Boolean.TRUE.equals(c.get("current"))));
    byte[] snapshot = atlas.backup();
    assertEquals("SQLite format 3", new String(snapshot, 0, 15, StandardCharsets.US_ASCII));
    assertEquals("not_found", assertThrows(AtlasException.class, () -> atlas.evictNode("no-such-node")).code());
  }

  @Test
  void rejectsNonOriginEndpoints() {
    assertThrows(IllegalArgumentException.class, () -> new AtlasClient("ftp://example", "t"));
    assertThrows(IllegalArgumentException.class, () -> new AtlasClient("https://example/api", "t"));
  }

  @Test
  void unreachableNodeHasNoStatus() {
    AtlasClient offline = new AtlasClient("http://127.0.0.1:9", "t");
    AtlasException error = assertThrows(AtlasException.class, offline::node);
    assertNull(error.status());
  }

  @Test
  void jsonRoundTrip() {
    String text = "{\"a\":[1,2.5,true,null,\"x\\\"y\\u00e9\\n\"],\"b\":{\"c\":-3}}";
    Map<String, Object> parsed = Json.parseObject(text);
    assertEquals(Arrays.asList(1L, 2.5, true, null, "x\"yé\n"), parsed.get("a"));
    assertEquals(text.replace("\\u00e9", "é"), Json.write(parsed));
    assertThrows(IllegalArgumentException.class, () -> Json.parse("{\"a\":}"));
    assertThrows(IllegalArgumentException.class, () -> Json.parse("[1] x"));
  }
}
