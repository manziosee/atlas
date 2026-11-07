"use client";

import { usePathname } from "next/navigation";
import { useEffect } from "react";

const visitorStorageKey = "atlas:analytics:visitor";
const pageViewEndpoint = "/analytics-api/v1/telemetry/pageview";

function visitorID(): string | null {
  try {
    const saved = localStorage.getItem(visitorStorageKey);
    if (saved && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(saved)) return saved;
    const created = crypto.randomUUID();
    localStorage.setItem(visitorStorageKey, created);
    return created;
  } catch (error) {
    console.warn("Atlas could not access local storage for anonymous analytics.", error);
    return null;
  }
}

export function PageViewTelemetry() {
  const pathname = usePathname();

  useEffect(() => {
    if (!pathname) return;
    const id = visitorID();
    if (!id) return;
    void fetch(pageViewEndpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ visitor_id: id, path: pathname }),
      keepalive: true,
    }).then((response) => {
      if (!response.ok) throw new Error(`Analytics endpoint returned ${response.status}`);
    }).catch((error: unknown) => {
      console.warn("Atlas could not record an anonymous page view.", error);
    });
  }, [pathname]);

  return null;
}
