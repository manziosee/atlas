import type { NextConfig } from "next";
import { execSync } from "node:child_process";
import pkg from "./package.json" with { type: "json" };

// Shown in the footer as v<version>+<commit>. Netlify sets COMMIT_REF.
function commit() {
  const fromEnv = process.env.COMMIT_REF || process.env.GITHUB_SHA;
  if (fromEnv) return fromEnv.slice(0, 7);
  try {
    return execSync("git rev-parse --short=7 HEAD", { stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
  } catch {
    return "local";
  }
}

// The console is a static site: every page runs in the browser and talks to
// an Atlas node directly. `next build` writes it to out/ for Netlify or any
// static host. In development, /demo-api is proxied to the public demo
// cluster the same way netlify.toml does in production.
const demoOrigin = process.env.ATLAS_DEMO_ORIGIN || "https://atlas-edge.fly.dev";
const analyticsOrigin = (process.env.ATLAS_ANALYTICS_ORIGIN || "http://127.0.0.1:8080").replace(/\/+$/, "");
const isDev = process.env.NODE_ENV !== "production";

const config: NextConfig = {
  output: isDev ? undefined : "export",
  trailingSlash: true,
  images: { unoptimized: true },
  poweredByHeader: false,
  env: {
    NEXT_PUBLIC_BUILD_VERSION: pkg.version,
    NEXT_PUBLIC_BUILD_COMMIT: commit(),
  },
  // Keep API paths such as /demo-api/v1/members free of the trailing-slash
  // redirect that page URLs get.
  skipTrailingSlashRedirect: true,
  agentRules: false,
  ...(isDev && {
    async rewrites() {
      return [
        { source: "/demo-api/:path*", destination: `${demoOrigin}/:path*` },
        { source: "/analytics-api/:path*", destination: `${analyticsOrigin}/:path*` },
      ];
    },
  }),
};

export default config;
