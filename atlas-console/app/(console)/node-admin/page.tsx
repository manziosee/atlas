"use client";

import { useEffect, useState } from "react";
import { DatabaseBackup, Loader2, ShieldAlert, Trash2 } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { can, useConnection } from "@/lib/connection";
import { explain, type Credential } from "@/lib/api";
import { downloadBlob, memberTone, timestamp } from "@/lib/format";
import { CodeBlock } from "@/components/copy-button";
import { ConfirmDialog, Empty, Notice, PageHeader, Skeleton, StatusBadge } from "@/components/console/ui";
import { useToast } from "@/components/console/toast";

export default function AdminPage() {
  const { conn, client } = useConnection();
  const { members, services, refresh } = useCluster();
  const toast = useToast();
  const admin = can(conn, "admin");
  const [creds, setCreds] = useState<Credential[] | null>(null);
  const [credError, setCredError] = useState<string | null>(null);
  const [backingUp, setBackingUp] = useState(false);
  const [evict, setEvict] = useState("");
  const [confirmEvict, setConfirmEvict] = useState(false);

  useEffect(() => {
    if (!admin || !client) return;
    client
      .credentials()
      .then(setCreds)
      .catch((err) => setCredError(explain(err)));
  }, [admin, client]);

  if (!admin) {
    return (
      <>
        <PageHeader title="Node admin" desc="Backups, credentials and evicting failed nodes on the node you are connected to." />
        <div className="panel p-6">
          <ShieldAlert className="h-6 w-6 text-warn" />
          <h2 className="mt-4 font-medium">This needs an admin token</h2>
          <p className="mt-2 max-w-xl text-sm text-muted">
            {conn?.mode === "demo"
              ? "The public demo is read-only, so admin tools are hidden. Run your own node to try them: dev mode gives you an admin token."
              : "Your token can view the cluster but not administer it. Disconnect and connect again with a credential that has the admin scope."}
          </p>
          <ul className="mt-5 space-y-2 text-sm text-muted">
            <li>• List configured credentials and their scopes (never the secrets)</li>
            <li>• Download a consistent SQLite backup of the registry</li>
            <li>• Evict a node that has failed for good, with its services</li>
          </ul>
        </div>
      </>
    );
  }

  const evictable = members.filter((m) => m.state !== "alive");
  const target = members.find((m) => m.id === evict);

  return (
    <>
      <PageHeader title="Node admin" desc="Operations that need the admin scope. They act on the node you're connected to." />
      <div className="grid gap-6 xl:grid-cols-2">
        <section className="panel p-5 xl:col-span-2" aria-labelledby="creds-title">
          <h2 id="creds-title" className="font-medium">
            Credentials
          </h2>
          <p className="mt-1 text-sm text-muted">Fingerprints match <span className="font-mono">token_id</span> in the node&apos;s access logs. Secrets are never returned.</p>
          {credError ? (
            <div className="mt-4">
              <Notice tone="error">{credError}</Notice>
            </div>
          ) : creds === null ? (
            <div className="mt-4 space-y-2">
              <Skeleton className="h-8" />
              <Skeleton className="h-8" />
            </div>
          ) : creds.length === 0 ? (
            <Empty title="No credentials" desc="This node has no configured credentials." />
          ) : (
            <div className="mt-4 overflow-x-auto">
              <table className="w-full min-w-[640px] text-sm">
                <thead className="border-b border-line text-left text-xs text-faint">
                  <tr>
                    <th scope="col" className="py-2 pr-4 font-normal">Fingerprint</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Scopes</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Source</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Expires</th>
                    <th scope="col" className="py-2 font-normal">Status</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {creds.map((c) => (
                    <tr key={c.id}>
                      <td className="py-2.5 pr-4 font-mono">
                        {c.id}
                        {c.current && <span className="ml-2 rounded bg-lime/15 px-1.5 py-0.5 font-sans text-[0.68rem] text-lime">you</span>}
                      </td>
                      <td className="py-2.5 pr-4 font-mono">{c.scopes.join(", ")}</td>
                      <td className="py-2.5 pr-4 text-muted">{c.source === "static" ? "ATLAS_API_TOKENS" : "ATLAS_API_CREDENTIALS"}</td>
                      <td className="py-2.5 pr-4 text-muted">{c.expires_at ? timestamp(c.expires_at) : "never"}</td>
                      <td className="py-2.5">{c.expired ? <StatusBadge tone="bad">expired</StatusBadge> : <StatusBadge tone="ok">active</StatusBadge>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="mt-5">
            <Notice tone="info" title="Creating and revoking credentials">
              The API doesn&apos;t create or revoke credentials, so neither does the console. Edit <span className="font-mono">ATLAS_API_CREDENTIALS</span> on each node and send
              it <span className="font-mono">SIGHUP</span> to reload without a restart.
            </Notice>
          </div>
        </section>

        <section className="panel p-5" aria-labelledby="backup-title">
          <DatabaseBackup className="h-5 w-5 text-teal" />
          <h2 id="backup-title" className="mt-3 font-medium">
            Backup
          </h2>
          <p className="mt-1 text-sm text-muted">
            Download a consistent SQLite snapshot of this node&apos;s registry ({services.length} instances). Keep it somewhere safe; it contains service addresses.
          </p>
          <button
            className="btn btn-lime mt-4"
            disabled={backingUp}
            onClick={async () => {
              setBackingUp(true);
              try {
                const blob = await client!.backup();
                const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
                downloadBlob(blob, `atlas-${conn!.info.node.id}-${stamp}.db`);
                toast("ok", `Backup downloaded (${(blob.size / 1024).toFixed(1)} KiB).`);
              } catch (err) {
                toast("error", explain(err));
              } finally {
                setBackingUp(false);
              }
            }}
          >
            {backingUp ? <Loader2 className="h-4 w-4 animate-spin" /> : <DatabaseBackup className="h-4 w-4" />} Download backup
          </button>
          <div className="mt-4">
            <CodeBlock title="restore to a new database path" code="atlas-cli db-restore -input atlas-backup.db -db /data/restored.db" />
          </div>
        </section>

        <section className="panel p-5" aria-labelledby="evict-title">
          <Trash2 className="h-5 w-5 text-bad" />
          <h2 id="evict-title" className="mt-3 font-medium">
            Evict a failed node
          </h2>
          <p className="mt-1 text-sm text-muted">Removes a node that is gone for good, and deletes the services it owned. Alive nodes can&apos;t be evicted; they&apos;d rejoin at once.</p>
          {evictable.length === 0 ? (
            <p className="mt-4 flex items-center gap-2 text-sm text-muted">
              <StatusBadge tone="ok">nothing to evict</StatusBadge> Every member is alive.
            </p>
          ) : (
            <div className="mt-4 flex flex-wrap gap-3">
              <label className="min-w-[200px] flex-1 text-sm">
                <span className="sr-only">Node to evict</span>
                <select className="field" value={evict} onChange={(e) => setEvict(e.target.value)}>
                  <option value="">Choose a node…</option>
                  {evictable.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.id} ({m.state}, {m.region || "no region"})
                    </option>
                  ))}
                </select>
              </label>
              <button className="btn btn-danger" disabled={!evict} onClick={() => setConfirmEvict(true)}>
                Evict
              </button>
            </div>
          )}
          {target && (
            <p className="mt-3 text-sm text-muted">
              <StatusBadge tone={memberTone[target.state]}>{target.state}</StatusBadge> owns {services.filter((s) => s.node_id === target.id).length} services.
            </p>
          )}
        </section>
      </div>

      <ConfirmDialog
        open={confirmEvict}
        onOpenChange={setConfirmEvict}
        title="Evict this node?"
        confirmLabel="Evict node"
        typeToConfirm={evict}
        onConfirm={async () => {
          try {
            const result = await client!.evict(evict);
            toast("ok", `Evicted ${result.node_id}; ${result.services_removed} services removed.`);
            setEvict("");
            await refresh();
          } catch (err) {
            toast("error", explain(err));
          }
        }}
      >
        <p>
          <span className="font-mono text-fg">{evict}</span> and every service it owned will be removed from the registry on all nodes. This can&apos;t be undone.
        </p>
      </ConfirmDialog>
    </>
  );
}
