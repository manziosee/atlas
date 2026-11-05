import { describe, expect, it } from "vitest";
import { docPages, extractSection, pageMarkdown, renderMarkdown, rewriteHref, shiftHeadings } from "@/lib/content";

describe("docs content", () => {
  it("extracts a section up to the next heading of the same level, ignoring code fences", () => {
    const md = "# T\n## A\nalpha\n```\n## not a heading\n```\n### A.1\nsub\n## B\nbeta";
    expect(extractSection(md, "## A")).toBe("alpha\n```\n## not a heading\n```\n### A.1\nsub");
    expect(extractSection(md, "### A.1")).toBe("sub");
    expect(extractSection(md, "## Missing")).toBeNull();
  });

  it("shifts headings outside code", () => {
    expect(shiftHeadings("### x\n```\n# keep\n```\n## y", -1)).toBe("## x\n```\n# keep\n```\n# y");
  });

  it("routes links to site pages, GitHub, or keeps them", () => {
    expect(rewriteHref("docs/DOCKER_AGENT.md", "README.md")).toBe("/docs/docker-agent/");
    expect(rewriteHref("../../../docs/KUBERNETES.md", "deploy/helm/atlas/README.md")).toBe("/docs/kubernetes/");
    expect(rewriteHref("#configuration", "README.md")).toBe("/docs/configuration/");
    expect(rewriteHref("#labels", "docs/DOCKER_AGENT.md")).toBe("#labels");
    expect(rewriteHref("deploy/docker-agent/compose.yaml", "docs/DOCKER_AGENT.md")).toBe(
      "https://github.com/manziosee/atlas/blob/main/docs/deploy/docker-agent/compose.yaml",
    );
    expect(rewriteHref("../deploy/docker-agent/compose.yaml", "docs/DOCKER_AGENT.md")).toBe("https://github.com/manziosee/atlas/blob/main/deploy/docker-agent/compose.yaml");
    expect(rewriteHref("atlas-console/", "README.md")).toBe("https://github.com/manziosee/atlas/tree/main/atlas-console");
    expect(rewriteHref("SECURITY.md", "README.md")).toBe("/security/");
    expect(rewriteHref("https://x.dev", "README.md")).toBe("https://x.dev");
  });

  it("renders headings with anchors and a table of contents", () => {
    const r = renderMarkdown("## Hello `world`\n\ntext\n\n### Sub & more\n\n| a |\n|---|\n| 1 |", "README.md");
    expect(r.headings).toEqual([
      { depth: 2, text: "Hello world", id: "hello-world" },
      { depth: 3, text: "Sub & more", id: "sub--more" },
    ]);
    expect(r.html).toContain('<h2 id="hello-world">');
    expect(r.html).toContain('<div class="table-wrap"><table>');
  });

  it("every doc page has content in this repository", () => {
    for (const page of docPages) {
      const { md, files } = pageMarkdown(page);
      expect(files.length, `${page.slug} sources`).toBe(page.sources.length);
      expect(md.length, `${page.slug} content`).toBeGreaterThan(100);
    }
  });
});

describe("API reference", () => {
  it("parses every endpoint of the OpenAPI description with scopes", async () => {
    const { loadApiSpec } = await import("@/lib/openapi");
    const spec = loadApiSpec()!;
    expect(spec.title).toBe("Atlas Node API");
    const find = (m: string, p: string) => spec.endpoints.find((e) => e.method === m && e.path === p);
    expect(find("GET", "/healthz")?.scope).toBe("none");
    expect(find("GET", "/v1/topology")?.params.map((p) => p.name)).toEqual(["scope"]);
    expect(find("POST", "/v1/services")?.body).toBe("ServiceSpec");
    expect(find("DELETE", "/v1/admin/nodes/{id}")?.scope).toBe("admin");
    expect(find("PATCH", "/v1/services/{id}")?.params.map((p) => p.name)).toEqual(["id", "If-Match"]);
    expect(spec.errorCodes).toContain("insufficient_scope");
    expect(spec.schemas.find((s) => s.name === "Service")?.props.some((p) => p.name === "draining")).toBe(true);
  });
});
