"use client";

import { useMemo } from "react";
import { geoGraticule10, geoNaturalEarth1, geoPath } from "d3-geo";
import { feature } from "topojson-client";
import type { GeometryCollection, Topology } from "topojson-specification";
import land110 from "world-atlas/land-110m.json";
import { band, bandColor, formatMs, type MapEdge, type MapNode } from "@/lib/topology";
import { memberTone } from "@/lib/format";

const W = 960;
const H = 500;
const stateFill = { ok: "#6ee7a0", warn: "#fbbf4f", bad: "#ff7a7a", muted: "#5d7070", info: "#6cc4ff" } as const;

/** Nodes at their locations on a world map, with great-circle links coloured by latency. */
export function WorldMap({ nodes, edges, selected, onSelect }: { nodes: MapNode[]; edges: MapEdge[]; selected: string | null; onSelect: (id: string) => void }) {
  const { landPath, graticule, project, path } = useMemo(() => {
    const projection = geoNaturalEarth1().fitSize([W, H], { type: "Sphere" });
    const p = geoPath(projection);
    const topology = land110 as unknown as Topology<{ land: GeometryCollection }>;
    return {
      landPath: p(feature(topology, topology.objects.land)) ?? "",
      graticule: p(geoGraticule10()) ?? "",
      project: (lat: number, lon: number) => projection([lon, lat]) as [number, number],
      path: p,
    };
  }, []);

  // Spread nodes that share a spot (same region) on a small ring.
  const placed = useMemo(() => {
    const groups = new Map<string, MapNode[]>();
    for (const n of nodes) {
      if (!n.position) continue;
      const k = n.position.join(",");
      groups.set(k, [...(groups.get(k) ?? []), n]);
    }
    const out = new Map<string, { x: number; y: number; node: MapNode }>();
    for (const group of groups.values()) {
      group.forEach((n, i) => {
        const [x, y] = project(n.position![0], n.position![1]);
        const r = group.length > 1 ? 9 + group.length : 0;
        const angle = (2 * Math.PI * i) / group.length;
        out.set(n.id, { x: x + r * Math.cos(angle), y: y + r * Math.sin(angle), node: n });
      });
    }
    return out;
  }, [nodes, project]);
  const unplaced = nodes.filter((n) => !n.position);

  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label={`World map with ${placed.size} located nodes`}>
        <defs>
          <radialGradient id="ocean" cx="50%" cy="40%" r="70%">
            <stop offset="0" stopColor="#0a1a1e" />
            <stop offset="1" stopColor="#04090b" />
          </radialGradient>
        </defs>
        <path d={path({ type: "Sphere" }) ?? ""} fill="url(#ocean)" stroke="rgb(255 255 255 / 0.08)" />
        <path d={graticule} fill="none" stroke="rgb(255 255 255 / 0.04)" />
        <path d={landPath} fill="#13262b" stroke="rgb(47 211 181 / 0.25)" strokeWidth={0.5} />
        {edges.map((e) => {
          const a = placed.get(e.a);
          const b = placed.get(e.b);
          if (!a || !b) return null;
          const colour = bandColor[band(e.rtt)!];
          const active = selected === e.a || selected === e.b;
          // Same spot: a short straight link; otherwise the great circle.
          const sameSpot = a.node.position!.join() === b.node.position!.join();
          const d = sameSpot
            ? `M${a.x},${a.y}L${b.x},${b.y}`
            : (path({ type: "LineString", coordinates: [[a.node.position![1], a.node.position![0]], [b.node.position![1], b.node.position![0]]] }) ?? "");
          return (
            <path key={`${e.a}-${e.b}`} d={d} fill="none" stroke={colour} strokeWidth={active ? 2.2 : 1.2} strokeOpacity={selected && !active ? 0.15 : 0.75}>
              <title>{`${e.a} ↔ ${e.b}: ${formatMs(e.rtt)}`}</title>
            </path>
          );
        })}
        {[...placed.values()].map(({ x, y, node }) => {
          const fill = stateFill[memberTone[node.state] ?? "muted"];
          const on = selected === node.id;
          return (
            <g key={node.id} transform={`translate(${x},${y})`} className="cursor-pointer" onClick={() => onSelect(node.id)} role="button" tabIndex={0} aria-label={`${node.id}, ${node.state}, ${node.region}`} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onSelect(node.id)}>
              <circle r={on ? 14 : 10} fill={fill} opacity={0.18}>
                {node.state === "alive" && <animate attributeName="r" values="8;14;8" dur="3s" repeatCount="indefinite" />}
              </circle>
              <circle r={on ? 6.5 : 5} fill={fill} stroke={on ? "#eef4f2" : "#04090b"} strokeWidth={1.5} />
              <title>{`${node.id} · ${node.state} · ${node.region || "no region"}`}</title>
            </g>
          );
        })}
      </svg>
      {unplaced.length > 0 && (
        <p className="mt-3 text-xs text-muted">
          Not on the map (unknown region): {unplaced.map((n) => n.id).join(", ")}. Set <span className="font-mono">ATLAS_LOCATION=lat,lon</span> on those nodes.
        </p>
      )}
    </div>
  );
}
