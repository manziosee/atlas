import type { Metadata } from "next";
import { OpsApp } from "@/components/ops/ops-app";

// The owner's private area for the whole site: sign-in, then usage, where
// Atlas is used and the health of the hosted cluster. Not linked anywhere.
export const metadata: Metadata = {
  title: "Admin",
  robots: { index: false, follow: false, nocache: true, googleBot: { index: false, follow: false } },
  referrer: "no-referrer",
};

export default function OpsPage() {
  return <OpsApp />;
}
