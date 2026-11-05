import type { AtlasEvent, Health, MemberState } from "./api";

export function cn(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(" ");
}

export function relative(at: string | number | undefined | null, now = Date.now()): string {
  if (!at) return "—";
  const t = typeof at === "number" ? at : Date.parse(at);
  if (Number.isNaN(t) || t <= 0) return "—";
  const s = Math.round((now - t) / 1000);
  if (s < 0) return "just now";
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h ago`;
}

export function timestamp(at?: string | number) {
  if (!at) return "—";
  const d = new Date(at);
  return Number.isNaN(d.getTime()) || d.getTime() <= 0 ? "—" : d.toLocaleString();
}

export type Tone = "ok" | "warn" | "bad" | "muted" | "info";

export const memberTone: Record<MemberState, Tone> = { alive: "ok", suspect: "warn", dead: "bad", left: "muted" };
export const healthTone: Record<Health, Tone> = { healthy: "ok", unknown: "warn", unhealthy: "bad" };

/** One line describing an event for the activity feed. */
export function describeEvent(e: AtlasEvent): { text: string; tone: Tone } {
  if (e.kind === "member" && e.member) {
    const m = e.member;
    switch (e.type) {
      case "join":
        return { text: `${m.id} joined the cluster (${m.region || "no region"})`, tone: "ok" };
      case "reap":
        return { text: `${m.id} was removed from the member list`, tone: "muted" };
      default:
        return { text: `${m.id} is now ${m.state}`, tone: memberTone[m.state] ?? "info" };
    }
  }
  if (e.kind === "service" && e.service) {
    const s = e.service;
    const who = `${s.name} (${s.id})`;
    switch (e.type) {
      case "service.registered":
        return { text: `${who} registered at ${s.address}`, tone: "ok" };
      case "service.deregistered":
        return { text: `${who} deregistered`, tone: "muted" };
      case "service.health":
        return { text: `${who} is ${s.health}`, tone: healthTone[s.health] };
      default:
        return { text: `${who} updated${s.draining ? " — draining" : ""} (revision ${s.revision})`, tone: "info" };
    }
  }
  return { text: e.type, tone: "info" };
}

export function downloadBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
