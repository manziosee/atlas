import Link from "next/link";
import { Wordmark } from "@/components/logo";
import { REPO_URL } from "@/lib/demo";

const columns = [
  {
    title: "Product",
    links: [
      { href: "/connect/", label: "Console" },
      { href: "/#demo", label: "Live demo" },
      { href: "/#features", label: "Features" },
      { href: "/docs/quick-start/", label: "Quick start" },
    ],
  },
  {
    title: "Docs",
    links: [
      { href: "/docs/", label: "Documentation" },
      { href: "/api/", label: "API reference" },
      { href: "/docs/kubernetes/", label: "Kubernetes & Helm" },
      { href: "/docs/terraform/", label: "Terraform" },
    ],
  },
  {
    title: "Project",
    links: [
      { href: "/about/", label: "About" },
      { href: "/contributing/", label: "Contributing" },
      { href: "/security/", label: "Security" },
      { href: "/terms/", label: "Terms" },
      { href: REPO_URL, label: "GitHub" },
    ],
  },
];

export function SiteFooter() {
  const version = process.env.NEXT_PUBLIC_BUILD_VERSION ?? "0.0.0";
  const commit = process.env.NEXT_PUBLIC_BUILD_COMMIT ?? "local";
  return (
    <footer className="border-t border-line bg-bg">
      <div className="mx-auto max-w-6xl px-4 py-12 sm:px-6">
        <div className="grid gap-10 md:grid-cols-[1.4fr_repeat(3,1fr)]">
          <div>
            <Wordmark />
            <p className="mt-3 max-w-xs text-sm text-muted">
              Distributed service discovery with no central registry. Every node knows which services are ready.
            </p>
          </div>
          {columns.map((col) => (
            <nav key={col.title} aria-label={col.title}>
              <p className="text-xs font-medium uppercase tracking-wider text-faint">{col.title}</p>
              <ul className="mt-3 space-y-2 text-sm">
                {col.links.map((l) => (
                  <li key={l.label}>
                    {l.href.startsWith("http") ? (
                      <a href={l.href} target="_blank" rel="noreferrer" className="text-muted hover:text-fg">
                        {l.label}
                      </a>
                    ) : (
                      <Link href={l.href} className="text-muted hover:text-fg">
                        {l.label}
                      </Link>
                    )}
                  </li>
                ))}
              </ul>
            </nav>
          ))}
        </div>

        <div className="mt-12 space-y-3 border-t border-line pt-6 text-xs leading-relaxed text-faint">
          <p className="text-muted">
            Built with <span aria-label="love">💚</span> by Manzi Osee and the Atlas contributors. Atlas is free, open-source software under the Apache 2.0
            licence: the discovery layer we wanted for our own clusters, open to every team that runs services across more than one machine.
          </p>
          <p>
            Atlas is an independent open-source project and is not affiliated with, endorsed by, or sponsored by HashiCorp, Inc., the Linux Foundation,
            the Cloud Native Computing Foundation, Docker, Inc., Fly.io or Netlify. Kubernetes and Helm are trademarks of The Linux Foundation. Terraform
            and Consul are trademarks of HashiCorp, Inc. Docker is a trademark of Docker, Inc. Other names are trademarks of their respective owners.
          </p>
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>© {new Date().getFullYear()} Atlas</span>
            <span aria-hidden>·</span>
            <a href={`${REPO_URL}/commit/${commit}`} target="_blank" rel="noreferrer" className="font-mono hover:text-muted" title="Version and commit of this site">
              {`v${version}+${commit}`}
            </a>
          </p>
        </div>
      </div>
    </footer>
  );
}
