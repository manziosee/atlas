import type { NextConfig } from "next";

// The console is a static site: every page runs in the browser and talks to
// an Atlas node directly. `next build` writes it to out/ for Netlify or any
// static host. In development, /demo-api is proxied to the public demo
// cluster the same way netlify.toml does in production.
const demoOrigin = process.env.ATLAS_DEMO_ORIGIN || "https://atlas-edge.fly.dev";
const isDev = process.env.NODE_ENV !== "production";

const config: NextConfig = {
  output: isDev ? undefined : "export",
  trailingSlash: true,
  images: { unoptimized: true },
  poweredByHeader: false,
  // Keep API paths such as /demo-api/v1/members free of the trailing-slash
  // redirect that page URLs get.
  skipTrailingSlashRedirect: true,
  agentRules: false,
  ...(isDev && {
    async rewrites() {
      return [{ source: "/demo-api/:path*", destination: `${demoOrigin}/:path*` }];
    },
  }),
};

export default config;
