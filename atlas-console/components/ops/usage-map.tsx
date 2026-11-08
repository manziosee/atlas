"use client";

import { useMemo } from "react";
import { geoGraticule10, geoNaturalEarth1, geoPath } from "d3-geo";
import { feature } from "topojson-client";
import type { GeometryCollection, Topology } from "topojson-specification";
import land110 from "world-atlas/land-110m.json";
import { countryCentres } from "@/lib/countries";

const W = 960;
const H = 470;

export interface CountryValue {
  country: string;
  value: number;
}

/** Dots sized by value on a world map, one per country. */
export function UsageMap({ data, color, label }: { data: CountryValue[]; color: string; label: string }) {
  const { land, graticule, sphere, project } = useMemo(() => {
    const projection = geoNaturalEarth1().fitSize([W, H], { type: "Sphere" });
    const path = geoPath(projection);
    const topology = land110 as unknown as Topology<{ land: GeometryCollection }>;
    return {
      land: path(feature(topology, topology.objects.land)) ?? "",
      graticule: path(geoGraticule10()) ?? "",
      sphere: path({ type: "Sphere" }) ?? "",
      project: (lat: number, lon: number) => projection([lon, lat]) as [number, number],
    };
  }, []);
  const max = Math.max(1, ...data.map((d) => d.value));
  const dots = data
    .filter((d) => d.value > 0 && countryCentres[d.country])
    .map((d) => {
      const [x, y] = project(...countryCentres[d.country]);
      return { ...d, x, y, r: 4 + 18 * Math.sqrt(d.value / max) };
    })
    .sort((a, b) => b.r - a.r); // small dots drawn on top

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label={`${label} by country`}>
      <path d={sphere} fill="#071216" stroke="rgb(255 255 255 / 0.08)" />
      <path d={graticule} fill="none" stroke="rgb(255 255 255 / 0.04)" />
      <path d={land} fill="#13262b" stroke="rgb(47 211 181 / 0.22)" strokeWidth={0.5} />
      {dots.map((d) => (
        <g key={d.country}>
          <circle cx={d.x} cy={d.y} r={d.r} fill={color} fillOpacity={0.28} stroke={color} strokeWidth={1.2}>
            <title>{`${d.country}: ${d.value} ${label}`}</title>
          </circle>
          <circle cx={d.x} cy={d.y} r={2} fill={color} />
        </g>
      ))}
    </svg>
  );
}
