// Turns api/v1/openapi.yaml into a structure the API reference page renders.
// Runs at build time, so the site always matches the spec in the repository.

import { parse } from "yaml";
import { readRepoFile } from "./content";

type Json = Record<string, unknown>;

export interface Param {
  name: string;
  in: string;
  required: boolean;
  type: string;
  description: string;
}

export interface Response {
  status: string;
  description: string;
  schema?: string;
}

export interface Endpoint {
  id: string;
  method: string;
  path: string;
  tag: string;
  summary: string;
  description: string;
  public: boolean;
  scope: "read" | "write" | "admin" | "none";
  params: Param[];
  body?: string;
  responses: Response[];
}

export interface SchemaProp {
  name: string;
  type: string;
  required: boolean;
  description: string;
}

export interface Schema {
  name: string;
  type: string;
  description: string;
  props: SchemaProp[];
  enum?: string[];
}

export interface ApiSpec {
  title: string;
  version: string;
  description: string;
  auth: string;
  servers: { url: string; description: string }[];
  tags: string[];
  endpoints: Endpoint[];
  schemas: Schema[];
  errorCodes: string[];
}

const METHODS = ["get", "post", "put", "patch", "delete"];

function typeOf(schema: Json | undefined, root: Json): string {
  if (!schema) return "";
  if (typeof schema.$ref === "string") return schema.$ref.split("/").pop()!;
  if (Array.isArray(schema.oneOf)) return (schema.oneOf as Json[]).map((s) => typeOf(s, root)).join(" | ");
  if (Array.isArray(schema.allOf)) return (schema.allOf as Json[]).map((s) => typeOf(s, root)).join(" & ");
  if (schema.type === "array") return `${typeOf(schema.items as Json, root) || "any"}[]`;
  if (Array.isArray(schema.enum)) return (schema.enum as string[]).map((v) => JSON.stringify(v)).join(" | ");
  if (schema.type === "object" && !schema.properties) {
    const values = schema.additionalProperties;
    return values && typeof values === "object" ? `map<string, ${typeOf(values as Json, root) || "any"}>` : "object";
  }
  const base = Array.isArray(schema.type) ? schema.type.join(" | ") : String(schema.type ?? "object");
  return schema.format ? `${base} (${schema.format})` : base;
}

function resolve(node: Json | undefined, root: Json): Json | undefined {
  let cur = node;
  for (let i = 0; cur && typeof cur.$ref === "string" && i < 8; i++) {
    const parts = (cur.$ref as string).replace(/^#\//, "").split("/");
    cur = parts.reduce<Json | undefined>((o, k) => (o ? (o[k] as Json) : undefined), root);
  }
  return cur;
}

function text(v: unknown) {
  return typeof v === "string" ? v.replace(/\s+/g, " ").trim() : "";
}

/** The minimum scope a request needs, mirroring the node's rules. */
function scopeFor(method: string, path: string, isPublic: boolean): Endpoint["scope"] {
  if (isPublic) return "none";
  if (path.startsWith("/v1/admin/")) return "admin";
  return method === "get" ? "read" : "write";
}

export function loadApiSpec(): ApiSpec | null {
  const raw = readRepoFile("api/v1/openapi.yaml");
  if (!raw) return null;
  const root = parse(raw) as Json;
  const info = (root.info ?? {}) as Json;
  const components = (root.components ?? {}) as Json;
  const schemasNode = (components.schemas ?? {}) as Record<string, Json>;
  const security = ((components.securitySchemes ?? {}) as Record<string, Json>).bearerAuth;

  const endpoints: Endpoint[] = [];
  for (const [path, item] of Object.entries((root.paths ?? {}) as Record<string, Json>)) {
    const shared = (item.parameters as Json[] | undefined) ?? [];
    for (const method of METHODS) {
      const op = item[method] as Json | undefined;
      // Internal operations are documented in the spec but not on the site.
      if (!op || op["x-internal"] === true) continue;
      const isPublic = Array.isArray(op.security) && (op.security as unknown[]).length === 0;
      const params = [...shared, ...((op.parameters as Json[] | undefined) ?? [])]
        .map((p) => resolve(p, root))
        .filter((p): p is Json => !!p)
        .map((p) => ({
          name: String(p.name),
          in: String(p.in),
          required: !!p.required,
          type: typeOf(p.schema as Json, root),
          description: text(p.description),
        }));
      const bodySchema = ((((op.requestBody as Json | undefined)?.content as Json | undefined)?.["application/json"] as Json | undefined)?.schema) as Json | undefined;
      const responses = Object.entries((op.responses ?? {}) as Record<string, Json>).map(([status, r]) => {
        const resolved = resolve(r, root) ?? {};
        const content = (resolved.content ?? {}) as Record<string, Json>;
        const first = Object.values(content)[0];
        return { status, description: text(resolved.description), schema: first ? typeOf(first.schema as Json, root) : undefined };
      });
      const tag = ((op.tags as string[] | undefined) ?? ["other"])[0];
      endpoints.push({
        id: `${method}-${path}`.replace(/[^a-zA-Z0-9]+/g, "-").replace(/-+$/, ""),
        method: method.toUpperCase(),
        path,
        tag,
        summary: text(op.summary),
        description: text(op.description),
        public: isPublic,
        scope: scopeFor(method, path, isPublic),
        params,
        body: bodySchema ? typeOf(bodySchema, root) : undefined,
        responses,
      });
    }
  }

  const internalSchemas = new Set(["UsageReport"]);
  const schemas: Schema[] = Object.entries(schemasNode).filter(([name]) => !internalSchemas.has(name)).map(([name, s]) => {
    const required = new Set((s.required as string[] | undefined) ?? []);
    return {
      name,
      type: typeOf({ ...s, $ref: undefined }, root),
      description: text(s.description),
      enum: Array.isArray(s.enum) ? (s.enum as string[]) : undefined,
      props: Object.entries((s.properties ?? {}) as Record<string, Json>).map(([prop, p]) => ({
        name: prop,
        type: typeOf(p, root),
        required: required.has(prop),
        description: text(p.description),
      })),
    };
  });

  const errorCode = ((schemasNode.Error?.properties as Json | undefined)?.code as Json | undefined)?.enum as string[] | undefined;
  // Spec order, keeping only tags that still have public endpoints.
  const tags = ((root.tags as Json[] | undefined) ?? []).map((t) => String(t.name)).filter((t) => endpoints.some((e) => e.tag === t));
  for (const e of endpoints) if (!tags.includes(e.tag)) tags.push(e.tag);

  return {
    title: String(info.title ?? "Atlas API"),
    version: String(info.version ?? ""),
    description: text(info.description),
    auth: text(security?.description),
    servers: ((root.servers as Json[] | undefined) ?? []).map((s) => ({ url: String(s.url), description: text(s.description) })),
    tags,
    endpoints,
    schemas,
    errorCodes: errorCode ?? [],
  };
}
