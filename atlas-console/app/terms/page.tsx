import type { Metadata } from "next";
import Link from "next/link";
import { ProsePage } from "@/components/prose-page";
import { REPO_URL } from "@/lib/demo";

export const metadata: Metadata = { title: "Terms", description: "Terms of use for this website, the console and the public demo, how we handle data, and the software licence." };

const headings = [
  { depth: 2, text: "Summary", id: "summary" },
  { depth: 2, text: "The software", id: "license" },
  { depth: 2, text: "This website and the console", id: "website" },
  { depth: 2, text: "The public demo", id: "demo" },
  { depth: 2, text: "Your data and privacy", id: "privacy" },
  { depth: 2, text: "Acceptable use", id: "acceptable-use" },
  { depth: 2, text: "No warranty", id: "warranty" },
  { depth: 2, text: "Trademarks", id: "trademarks" },
  { depth: 2, text: "Changes and contact", id: "contact" },
];

export default function TermsPage() {
  return (
    <ProsePage eyebrow="Terms" title="Terms of use" intro={<>Last updated 7 November 2026. Plain language first; these terms cover this website, the hosted console and the public demo cluster.</>} headings={headings}>
      <div className="prose mt-8">
        <h2 id="summary">Summary</h2>
        <ul>
          <li>Atlas is free, open-source software under the Apache License 2.0.</li>
          <li>This website has no accounts or third-party trackers. It records privacy-limited page views and anonymous daily unique-browser counts.</li>
          <li>The console sends your token only to the Atlas node you connect to, from your own browser.</li>
          <li>The demo is public, read-only and may be reset or taken down at any time.</li>
          <li>Everything is provided as is, without warranty.</li>
        </ul>

        <h2 id="license">The software</h2>
        <p>
          Atlas, including the node, the CLI, the agents, the Helm chart, the Terraform provider and this console, is licensed under the{" "}
          <a href={`${REPO_URL}/blob/main/LICENSE`} target="_blank" rel="noreferrer">
            Apache License, Version 2.0
          </a>
          . That licence, not these terms, governs how you may use, modify and redistribute the code. When you run Atlas yourself, you are responsible for
          operating it, securing it and complying with the laws that apply to you.
        </p>

        <h2 id="website">This website and the console</h2>
        <p>
          This site is a static website. The console runs in your browser: when you connect to a node, your browser talks to that node directly. A separate
          first-party endpoint receives only anonymous page-view events for this site; it never receives your token or your cluster&apos;s data.
        </p>
        <p>
          You are responsible for the nodes you connect to and the tokens you use. Only connect to nodes you&apos;re allowed to access, and treat API
          tokens like passwords.
        </p>

        <h2 id="demo">The public demo</h2>
        <p>
          The demo cluster exists so people can try Atlas. It uses a published, read-only token, so nothing in it can be changed through the console. Its
          data is sample data and may be reset, changed or removed at any time without notice. Don&apos;t rely on it for anything, and never send real or
          sensitive data to it.
        </p>

        <h2 id="privacy">Your data and privacy</h2>
        <ul>
          <li>
            <strong>Anonymous site analytics.</strong> The site sets no cookies and loads no third-party trackers or scripts. To count daily unique browsers,
            it stores a random identifier in your browser&apos;s local storage. The server hashes that ID together with the UTC day, discards the raw ID, and
            retains daily page totals and hashes for up to 90 days. Page paths are normalized; query strings, names, IP addresses and device fingerprints
            are not stored in the analytics database. Unique-browser counts are approximate (about 3% relative error), deduplicated across reachable
            nodes, and capped at 10,000 stored visitors per node per day. At most 256 page paths per node per day are retained; additional paths are
            grouped as &quot;/other&quot;. You can remove the identifier by clearing this site&apos;s local storage.
          </li>
          <li>
            <strong>Tokens.</strong> A token you enter is held in your browser&apos;s memory. If you tick &quot;Keep me connected&quot;, it&apos;s stored
            in that tab&apos;s session storage until the tab closes. It is only ever sent to the node you connect to.
          </li>
          <li>
            <strong>Hosting logs.</strong> Like any website, our hosting providers may keep standard request logs, such as IP address, time and page, for
            security and operations. These platform logs are separate from the analytics database and are governed by the hosting providers&apos; policies.
            Requests to the demo pass through the hosting provider&apos;s proxy and reach the demo nodes, which keep standard access logs.
          </li>
          <li>
            <strong>Your own nodes.</strong> Data in your Atlas cluster stays in your infrastructure, under your control.
          </li>
        </ul>

        <h2 id="acceptable-use">Acceptable use</h2>
        <p>
          Don&apos;t use this site or the demo to attack, overload or probe systems you aren&apos;t authorised to test, to try to gain access you
          weren&apos;t given, or to break the law. If you find a security issue, please report it privately as described on the{" "}
          <Link href="/security/">security page</Link>. We&apos;re glad to hear about it.
        </p>

        <h2 id="warranty">No warranty</h2>
        <p>
          The software, this website, the console and the demo are provided &quot;as is&quot;, without warranties or conditions of any kind, as set out
          in sections 7 and 8 of the Apache License 2.0. To the extent the law allows, the maintainers and contributors are not liable for any damages
          arising from their use.
        </p>

        <h2 id="trademarks">Trademarks</h2>
        <p>
          Atlas is an independent open-source project and is not affiliated with, endorsed by or sponsored by HashiCorp, Inc., the Linux Foundation, the
          Cloud Native Computing Foundation, Docker, Inc., Fly.io or Netlify. Kubernetes and Helm are trademarks of The Linux Foundation. Terraform and
          Consul are trademarks of HashiCorp, Inc. Docker is a trademark of Docker, Inc. Other names belong to their respective owners and are used here only
          to describe compatibility.
        </p>

        <h2 id="contact">Changes and contact</h2>
        <p>
          We may update these terms as the project changes; the date at the top shows the latest version, and the history is public in the repository.
          Questions: <a href="mailto:manziosee@gmail.com">manziosee@gmail.com</a>.
        </p>
      </div>
    </ProsePage>
  );
}
