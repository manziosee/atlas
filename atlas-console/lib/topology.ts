import type { ClusterTopology, Member, MemberState } from "./api";
import { locate } from "./geo";

export interface MapNode {
  id: string;
  region: string;
  state: MemberState;
  version?: string;
  /** [lat, lon], or null when the region is unknown and no location is set. */
  position: [number, number] | null;
  /** Why this node's own view is missing, if it is. */
  error?: string;
  services: number;
}

export interface MapEdge {
  a: string;
  b: string;
  /** a's measurement of b and b's of a, in milliseconds. */
  ab?: number;
  ba?: number;
  /** Mean of the available measurements. */
  rtt: number;
}

export interface TopologyModel {
  nodes: MapNode[];
  edges: MapEdge[];
  /** latency[from][to] in milliseconds, from each node's own view. */
  matrix: Record<string, Record<string, number | undefined>>;
}

/**
 * Merges every node's view into one model. Membership from the connected
 * node fills in nodes whose view couldn't be read.
 */
export function buildTopology(topology: ClusterTopology | null, members: Member[], servicesPerNode: Map<string, number>): TopologyModel {
  const nodes = new Map<string, MapNode>();
  const matrix: TopologyModel["matrix"] = {};
  const put = (id: string, region: string, state: MemberState, extra: Partial<MapNode> = {}) => {
    const prev = nodes.get(id);
    nodes.set(id, {
      id,
      region: region || prev?.region || "",
      state: prev?.state ?? state,
      position: prev?.position ?? locate(region),
      services: servicesPerNode.get(id) ?? 0,
      ...prev,
      ...extra,
    });
  };

  for (const m of members) put(m.id, m.region, m.state);
  for (const entry of topology?.nodes ?? []) {
    const view = entry.view;
    if (!view) {
      put(entry.id, nodes.get(entry.id)?.region ?? "", nodes.get(entry.id)?.state ?? "dead", { error: entry.error });
      continue;
    }
    put(view.node.id, view.node.region, view.node.state, {
      version: view.version,
      position: locate(view.node.region, view.location),
      state: nodes.get(view.node.id)?.state ?? view.node.state,
    });
    matrix[view.node.id] = {};
    for (const peer of view.peers) {
      if (!nodes.has(peer.id)) put(peer.id, peer.region ?? "", peer.state);
      matrix[view.node.id][peer.id] = peer.rtt_ms;
    }
  }

  const ids = [...nodes.keys()].sort();
  const edges: MapEdge[] = [];
  for (let i = 0; i < ids.length; i++) {
    for (let j = i + 1; j < ids.length; j++) {
      const a = ids[i];
      const b = ids[j];
      const ab = matrix[a]?.[b];
      const ba = matrix[b]?.[a];
      const known = [ab, ba].filter((v): v is number => typeof v === "number");
      if (known.length) edges.push({ a, b, ab, ba, rtt: known.reduce((x, y) => x + y, 0) / known.length });
    }
  }
  return { nodes: ids.map((id) => nodes.get(id)!), edges, matrix };
}

export type LatencyBand = "fast" | "ok" | "slow" | "bad";

/** Bands for colouring links: same region, same continent, far, very far. */
export function band(ms: number | undefined): LatencyBand | undefined {
  if (ms === undefined) return undefined;
  if (ms < 5) return "fast";
  if (ms < 50) return "ok";
  if (ms < 150) return "slow";
  return "bad";
}

export const bandColor: Record<LatencyBand, string> = {
  fast: "#6ee7a0",
  ok: "#b6f27a",
  slow: "#fbbf4f",
  bad: "#ff7a7a",
};

export function formatMs(ms: number | undefined) {
  if (ms === undefined) return "—";
  if (ms < 1) return `${(ms * 1000).toFixed(0)} µs`;
  if (ms < 10) return `${ms.toFixed(2)} ms`;
  return `${ms.toFixed(0)} ms`;
}
