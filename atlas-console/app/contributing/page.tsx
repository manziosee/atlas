import type { Metadata } from "next";
import { ProsePage } from "@/components/prose-page";
import { renderFile } from "@/lib/content";
import { REPO_URL } from "@/lib/demo";

export const metadata: Metadata = { title: "Contributing", description: "How to contribute to Atlas: ground rules, the development loop, pull requests and code style." };

export default function ContributingPage() {
  const doc = renderFile("CONTRIBUTING.md", { dropTitle: true });
  return (
    <ProsePage
      eyebrow="Contributing"
      title="Contribute to Atlas"
      intro={
        <>
          Atlas is open source and built in the open. Bug reports, docs fixes, new integrations and ideas are all welcome. Start with an{" "}
          <a className="text-teal underline" href={`${REPO_URL}/issues`} target="_blank" rel="noreferrer">
            issue
          </a>
          , or read the guide below.
        </>
      }
      html={doc?.html}
      headings={doc?.headings}
      source="CONTRIBUTING.md"
    />
  );
}
