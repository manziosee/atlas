"use client";

import { usePathname } from "next/navigation";
import { useEffect } from "react";
import { track } from "@/lib/telemetry";

export function PageViewTelemetry() {
  const pathname = usePathname();
  useEffect(() => {
    if (pathname) track({ type: "pageview", path: pathname });
  }, [pathname]);
  return null;
}
