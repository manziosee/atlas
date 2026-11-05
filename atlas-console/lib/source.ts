import type { Service } from "./api";

export type SourceKind = "kubernetes" | "docker" | "manual";

export interface Source {
  kind: SourceKind;
  /** The cluster or Docker host that registered it. */
  owner?: string;
  namespace?: string;
  workload?: string;
}

/** Where an instance came from, from the owner tags integrations add. */
export function sourceOf(service: Pick<Service, "tags">): Source {
  const tags = service.tags ?? [];
  const value = (prefix: string) => tags.find((t) => t.startsWith(prefix))?.slice(prefix.length);
  const cluster = value("atlas-k8s:");
  if (cluster !== undefined) {
    return { kind: "kubernetes", owner: cluster, namespace: value("k8s-namespace:"), workload: value("k8s-pod:") ?? value("k8s-service:") };
  }
  const host = value("atlas-agent:");
  if (host !== undefined) return { kind: "docker", owner: host };
  return { kind: "manual" };
}

export const sourceLabel: Record<SourceKind, string> = {
  kubernetes: "Kubernetes",
  docker: "Docker",
  manual: "API / Terraform",
};

/** Tags that only describe the source; the console shows them as a badge instead. */
export function isSourceTag(tag: string) {
  return /^(atlas-k8s|atlas-agent|k8s-namespace|k8s-service|k8s-pod):/.test(tag);
}
