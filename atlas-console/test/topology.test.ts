import { describe, expect, it } from "vitest";
import { band, buildTopology, formatMs } from "@/lib/topology";
import { isSourceTag, sourceOf } from "@/lib/source";
import type { ClusterTopology, Member } from "@/lib/api";

const member = (id: string, region: string, state: Member["state"] = "alive"): Member => ({ id, region, state, addr: `${id}:7946`, incarnation: 1 });

describe("buildTopology", () => {
  it("merges views into nodes, a latency matrix and undirected edges", () => {
    const topo: ClusterTopology = {
      truncated: false,
      at: "",
      nodes: [
        {
          id: "a",
          view: {
            node: member("a", "cdg"),
            version: "v1",
            at: "",
            location: { lat: 10, lon: 20 },
            peers: [
              { id: "b", state: "alive", rtt_ms: 2 },
              { id: "c", state: "suspect" },
            ],
          },
        },
        { id: "b", view: { node: member("b", "iad"), version: "v1", at: "", peers: [{ id: "a", state: "alive", rtt_ms: 4 }] } },
        { id: "c", error: "unreachable: timeout" },
      ],
    };
    const model = buildTopology(topo, [member("a", "cdg"), member("b", "iad"), member("c", "nowhere", "suspect")], new Map([["a", 3]]));
    expect(model.nodes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(model.nodes[0]).toMatchObject({ position: [10, 20], services: 3, version: "v1" });
    expect(model.nodes[1].position).toEqual([38.95, -77.45]); // iad from the region table
    expect(model.nodes[2]).toMatchObject({ position: null, error: "unreachable: timeout", state: "suspect" });
    expect(model.matrix.a.b).toBe(2);
    expect(model.edges).toEqual([{ a: "a", b: "b", ab: 2, ba: 4, rtt: 3 }]);
  });

  it("falls back to membership when the topology API is unavailable", () => {
    const model = buildTopology(null, [member("x", "fra"), member("y", "")], new Map());
    expect(model.nodes).toHaveLength(2);
    expect(model.edges).toEqual([]);
    expect(model.nodes[1].position).toBeNull();
  });
});

describe("latency helpers", () => {
  it("bands and formats", () => {
    expect([1, 20, 90, 200].map(band)).toEqual(["fast", "ok", "slow", "bad"]);
    expect(band(undefined)).toBeUndefined();
    expect(formatMs(0.25)).toBe("250 µs");
    expect(formatMs(3.456)).toBe("3.46 ms");
    expect(formatMs(87.4)).toBe("87 ms");
  });
});


describe("sourceOf", () => {
  it("recognises integration owner tags", () => {
    expect(sourceOf({ tags: ["atlas-k8s:prod", "k8s-namespace:shop", "k8s-service:payments", "k8s-pod:p-1", "team"] })).toEqual({
      kind: "kubernetes",
      owner: "prod",
      namespace: "shop",
      workload: "p-1",
    });
    expect(sourceOf({ tags: ["atlas-agent:host-a"] })).toEqual({ kind: "docker", owner: "host-a" });
    expect(sourceOf({ tags: ["primary"] })).toEqual({ kind: "manual" });
    expect(sourceOf({})).toEqual({ kind: "manual" });
    expect(isSourceTag("k8s-pod:x")).toBe(true);
    expect(isSourceTag("primary")).toBe(false);
  });
});
