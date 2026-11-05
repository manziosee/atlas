import type { Metadata } from "next";
import { ProsePage } from "@/components/prose-page";
import { renderFile, renderSection } from "@/lib/content";

export const metadata: Metadata = { title: "Security", description: "How Atlas protects gossip, the API and your tokens, its threat model, and how to report a vulnerability." };

export default function SecurityPage() {
  const overview = renderSection("README.md", "## Security");
  const policy = renderFile("SECURITY.md", { dropTitle: true });
  const html = [overview ? `<h2 id="overview">Overview</h2>${overview.html}` : "", policy?.html ?? ""].join("\n");
  const headings = [...(overview ? [{ depth: 2, text: "Overview", id: "overview" }] : []), ...(policy?.headings ?? [])];
  return (
    <ProsePage
      eyebrow="Security"
      title="Security at Atlas"
      intro={
        <>
          Atlas assumes the network between your regions is hostile. Here is how gossip, the API, the console and the integrations are protected, what we
          know is still missing, and how to report a problem privately.
        </>
      }
      html={html}
      headings={headings}
      source="SECURITY.md"
    />
  );
}
