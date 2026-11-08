import type { Metadata } from "next";
import { OpsApp } from "@/components/ops/ops-app";

// Served only under the secret path configured at deploy time (see
// scripts/write-redirects.mjs); /ops itself always answers 404.
export const metadata: Metadata = {
  title: "Operations",
  robots: { index: false, follow: false, nocache: true, googleBot: { index: false, follow: false } },
  referrer: "no-referrer",
};

export default function OpsPage() {
  return <OpsApp />;
}
