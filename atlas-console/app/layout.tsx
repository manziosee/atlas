import type { Metadata, Viewport } from "next";
import { Figtree, JetBrains_Mono } from "next/font/google";
import { ConnectionProvider } from "@/lib/connection";
import { PageViewTelemetry } from "@/components/analytics/page-view";
import "./globals.css";

const figtree = Figtree({ subsets: ["latin"], variable: "--font-figtree", display: "swap" });
const mono = JetBrains_Mono({ subsets: ["latin"], variable: "--font-jetbrains", display: "swap" });

export const metadata: Metadata = {
  title: { default: "Atlas — know which services are ready, across every node", template: "%s · Atlas" },
  description:
    "Atlas is a distributed service-discovery system. Every node keeps a replicated, health-aware view of your services, with no central registry. The Atlas Console shows cluster health, services, live events and lookups.",
  applicationName: "Atlas Console",
  openGraph: {
    title: "Atlas — distributed service discovery",
    description: "Health-aware service discovery with no central registry. Explore the live, read-only demo cluster.",
    type: "website",
  },
  twitter: { card: "summary" },
};

export const viewport: Viewport = { themeColor: "#04090b", colorScheme: "dark" };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${figtree.variable} ${mono.variable}`}>
      <body className="min-h-dvh">
        <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[100] focus:rounded-full focus:bg-lime focus:px-4 focus:py-2 focus:text-lime-ink">
          Skip to content
        </a>
        <ConnectionProvider>
          <PageViewTelemetry />
          {children}
        </ConnectionProvider>
      </body>
    </html>
  );
}
