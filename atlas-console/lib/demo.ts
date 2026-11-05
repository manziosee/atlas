// The public read-only demo cluster. In production the console reaches it
// through a same-origin proxy (netlify.toml, next.config.ts in dev), so the
// demo works without the node allowing this site's origin.
export const DEMO_URL = process.env.NEXT_PUBLIC_ATLAS_DEMO_URL || "/demo-api";
// Intentionally public: read scope only, published in the Atlas README.
export const DEMO_TOKEN = process.env.NEXT_PUBLIC_ATLAS_DEMO_TOKEN || "atlas-demo-read-only-public-token-2027";
export const REPO_URL = "https://github.com/manziosee/atlas";
export const DOCS = {
  readme: `${REPO_URL}#readme`,
  openapi: `${REPO_URL}/blob/main/api/v1/openapi.yaml`,
  architecture: `${REPO_URL}/blob/main/docs/ARCHITECTURE.md`,
  dockerAgent: `${REPO_URL}/blob/main/docs/DOCKER_AGENT.md`,
  runbooks: `${REPO_URL}/tree/main/docs/runbooks`,
  console: `${REPO_URL}/tree/main/atlas-console`,
  config: `${REPO_URL}#configuration`,
};
