"use client";

import { useMemo } from "react";
import { band, bandColor, formatMs, type MapEdge, type MapNode } from "@/lib/topology";
import { memberTone } from "@/lib/format";

const SIZE = 560;
const stateFill = { ok: "#6ee7a0", warn: "#fbbf4f", bad: "#ff7a7a", muted: "#5d7070", info: "#6cc4ff" } as const;

/**
 * The cluster as a graph: nodes on a ring grouped by region, links coloured
 * by measured latency. Every gossip peer probes every other, so the graph is
 * as dense as the cluster.
 */
export function ClusterGraph({ nodes, edges, selected, onSelect }: { nodes: MapNode[]; edges: MapEdge[]; selected: string | null; onSelect: (id: string) => void }) {
  const layout = useMemo(() => {
    const ordered = [...nodes].sort((a, b) => a.region.localeCompare(b.region) || a.id.localeCompare(b.id));
    const r = nodes.length <= 1 ? 0 : SIZE / 2 - 70;
    const pos = new Map<string, { x: number; y: number; angle: number }>();
    ordered.forEach((n, i) => {
      const angle = (2 * Math.PI * i) / Math.max(ordered.length, 1) - Math.PI / 2;
      pos.set(n.id, { x: SIZE / 2 + r * Math.cos(angle), y: SIZE / 2 + r * Math.sin(angle), angle });
    });
    return pos;
  }, [nodes]);
  const showLabels = nodes.length <= 24;
  const radius = nodes.length > 60 ? 4 : nodes.length > 20 ? 6 : 9;

  return (
    <svg viewBox={`0 0 ${SIZE} ${SIZE}`} className="mx-auto h-auto w-full max-w-[560px]" role="img" aria-label={`Cluster graph: ${nodes.length} nodes, ${edges.length} measured links`}>
      <circle cx={SIZE / 2} cy={SIZE / 2} r={SIZE / 2 - 70} fill="none" stroke="rgb(255 255 255 / 0.05)" strokeDasharray="3 6" />
      {edges.map((e) => {
        const a = layout.get(e.a)!;
        const b = layout.get(e.b)!;
        const active = selected === e.a || selected === e.b;
        // Curve links slightly toward the centre so dense graphs stay readable.
        const mx = (a.x + b.x) / 2;
        const my = (a.y + b.y) / 2;
        const cx = mx + (SIZE / 2 - mx) * 0.35;
        const cy = my + (SIZE / 2 - my) * 0.35;
        return (
          <path
            key={`${e.a}-${e.b}`}
            d={`M${a.x},${a.y} Q${cx},${cy} ${b.x},${b.y}`}
            fill="none"
            stroke={bandColor[band(e.rtt)!]}
            strokeWidth={active ? 2.4 : 1.2}
            strokeOpacity={selected && !active ? 0.1 : 0.7}
          >
            <title>{`${e.a} ↔ ${e.b}: ${formatMs(e.rtt)}`}</title>
          </path>
        );
      })}
      {nodes.map((n) => {
        const p = layout.get(n.id)!;
        const fill = stateFill[memberTone[n.state] ?? "muted"];
        const on = selected === n.id;
        const anchor = Math.cos(p.angle) > 0.2 ? "start" : Math.cos(p.angle) < -0.2 ? "end" : "middle";
        const lx = p.x + Math.cos(p.angle) * (radius + 8);
        const ly = p.y + Math.sin(p.angle) * (radius + 8) + 4;
        return (
          <g key={n.id} className="cursor-pointer" onClick={() => onSelect(n.id)} role="button" tabIndex={0} aria-label={`${n.id}, ${n.state}`} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onSelect(n.id)}>
            {n.error && <circle cx={p.x} cy={p.y} r={radius + 4} fill="none" stroke="#fbbf4f" strokeDasharray="2 3" />}
            <circle cx={p.x} cy={p.y} r={on ? radius + 3 : radius} fill={fill} stroke={on ? "#eef4f2" : "#04090b"} strokeWidth={2} />
            {showLabels && (
              <text x={lx} y={ly} textAnchor={anchor} className="fill-current font-mono text-[11px] text-muted">
                {n.id.length > 18 ? `${n.id.slice(0, 16)}…` : n.id}
              </text>
            )}
            <title>{`${n.id} · ${n.state} · ${n.region || "no region"}${n.error ? ` · ${n.error}` : ""}`}</title>
          </g>
        );
      })}
    </svg>
  );
}
