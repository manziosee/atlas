// Binds the ops core to Netlify: Blobs for state, the request context for
// the client IP and country.
import { getStore } from "@netlify/blobs";
import type { Context } from "@netlify/functions";
import type { Deps, KV, OpsRequest } from "./core";

function kv(): KV {
  const store = getStore({ name: "atlas-ops", consistency: "strong" });
  return {
    get: async <T>(key: string) => (await store.get(key, { type: "json" })) as T | null,
    set: async (key, value) => {
      await store.setJSON(key, value);
    },
    delete: (key) => store.delete(key),
  };
}

export function deps(): Deps {
  return { kv: kv(), env: process.env };
}

export function opsRequest(req: Request, context: Context): OpsRequest {
  return {
    method: req.method,
    url: req.url,
    headers: req.headers,
    json: () => req.json(),
    ip: context.ip,
    country: context.geo?.country?.code,
  };
}
