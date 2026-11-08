// Anonymous, first-party usage events for the hosted site (see the Terms).
// Off unless the build sets NEXT_PUBLIC_ATLAS_ANALYTICS_ENABLED=true, so
// self-hosted builds send nothing.

const visitorStorageKey = "atlas:analytics:visitor";
const endpoint = "/telemetry/collect";
export const telemetryEnabled = process.env.NEXT_PUBLIC_ATLAS_ANALYTICS_ENABLED === "true";

type Event = { type: "pageview"; path: string } | { type: "connect"; kind: "node" | "demo"; version?: string };

function visitorID(): string | null {
  try {
    const saved = localStorage.getItem(visitorStorageKey);
    if (saved && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(saved)) return saved;
    const created = crypto.randomUUID();
    localStorage.setItem(visitorStorageKey, created);
    return created;
  } catch {
    return null; // storage blocked: count nothing
  }
}

/** Pages marked private (the operations area) are never reported. */
function privatePage() {
  return typeof document !== "undefined" && document.querySelector("[data-atlas-private]") !== null;
}

export function track(event: Event) {
  if (!telemetryEnabled || typeof window === "undefined" || privatePage()) return;
  const id = visitorID();
  if (!id) return;
  void fetch(endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...event, visitor_id: id }),
    keepalive: true,
  }).catch(() => {
    // best effort
  });
}
