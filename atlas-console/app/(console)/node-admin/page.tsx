"use client";

import { useEffect, useState } from "react";
import { DatabaseBackup, Download, KeyRound, Loader2, ScrollText, ShieldAlert, Trash2 } from "lucide-react";
import { useCluster } from "@/lib/cluster";
import { can, useConnection } from "@/lib/connection";
import { explain, type AuditEntry, type Credential, type Scope } from "@/lib/api";
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
  const [audit, setAudit] = useState<AuditEntry[] | null>(null);
  const [auditError, setAuditError] = useState<string | null>(null);
  const [auditBefore, setAuditBefore] = useState<number | undefined>();
  const [loadingAuditMore, setLoadingAuditMore] = useState(false);
  const [backingUp, setBackingUp] = useState(false);
  const [exportingAudit, setExportingAudit] = useState(false);
  const [creatingCredential, setCreatingCredential] = useState(false);
  const [credentialName, setCredentialName] = useState("");
  const [credentialScopes, setCredentialScopes] = useState<Scope[]>(["read"]);
  const [credentialExpiryDays, setCredentialExpiryDays] = useState(90);
  const [issuedToken, setIssuedToken] = useState<string | null>(null);
  const [revokeCredential, setRevokeCredential] = useState<Credential | null>(null);
  const [evict, setEvict] = useState("");
  const [confirmEvict, setConfirmEvict] = useState(false);

  useEffect(() => {
    if (!admin || !client) return;
    client.credentials().then(setCreds).catch((err) => setCredError(explain(err)));
    client
      .audit( undefined, 50)
      .then((page) => {
        setAudit(page.entries);
        setAuditBefore(page.next_before);
      })
      .catch((err) => setAuditError(explain(err)));
  }, [admin, client]);

  if (!admin) {
    return (
      <>
        <PageHeader title="Node admin" desc="Manage cluster credentials and node-local administration on the node you're connected to." />
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
            <li>• Create and revoke expiring cluster-wide credentials</li>
            <li>• Review and export 90 days of local admin audit events</li>
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
      <PageHeader title="Node admin" desc="Credentials replicate cluster-wide; audit entries and backups are local to the node you're connected to." />
      <div className="grid gap-6 xl:grid-cols-2">
        <section className="panel p-5 xl:col-span-2" aria-labelledby="creds-title">
          <h2 id="creds-title" className="font-medium">
            Credentials
          </h2>
          <p className="mt-1 text-sm text-muted">All peers must support managed credentials. Replication and revocation converge asynchronously. Tokens are shown only once; fingerprints match <span className="font-mono">token_id</span> in access logs.</p>
          {issuedToken && (
            <div className="mt-4 rounded-xl border border-warn/40 bg-warn/10 p-4">
              <p className="font-medium text-warn">Copy this token now. It cannot be shown again.</p>
              <div className="mt-3"><CodeBlock title="new API token" code={issuedToken} /></div>
              <button type="button" className="btn btn-outline mt-3" onClick={() => setIssuedToken(null)}>Dismiss token</button>
            </div>
          )}
          <form
            className="mt-5 grid gap-3 rounded-xl border border-line p-4 sm:grid-cols-2"
            onSubmit={async (event) => {
              event.preventDefault();
              if (!client || !credentialName.trim() || credentialScopes.length === 0) return;
              setCreatingCredential(true);
              try {
                const created = await client.createCredential({
                  name: credentialName.trim(),
                  scopes: credentialScopes,
                  expires_at: new Date(Date.now() + credentialExpiryDays * 24 * 60 * 60 * 1000).toISOString(),
                });
                setIssuedToken(created.token);
                setCredentialName("");
                toast("ok", "Cluster credential created. Copy its token now.");
                const updated = await client.credentials();
                setCreds(updated);
                const auditPage = await client.audit(undefined, 50);
                setAudit(auditPage.entries);
                setAuditBefore(auditPage.next_before);
              } catch (err) {
                toast("error", explain(err));
              } finally {
                setCreatingCredential(false);
              }
            }}
          >
            <label className="text-sm sm:col-span-2">
              Credential name
              <input className="field mt-1.5" value={credentialName} maxLength={128} required onChange={(event) => setCredentialName(event.target.value)} placeholder="e.g. production deployer" />
            </label>
            <fieldset className="text-sm">
              <legend className="mb-2">Scopes</legend>
              <div className="flex flex-wrap gap-4">
                {(["read", "write", "admin"] as Scope[]).map((scope) => (
                  <label key={scope} className="flex items-center gap-2 text-muted">
                    <input
                      type="checkbox"
                      checked={credentialScopes.includes(scope)}
                      onChange={(event) => setCredentialScopes((current) => event.target.checked ? [...current, scope] : current.filter((value) => value !== scope))}
                    />
                    {scope}
                  </label>
                ))}
              </div>
            </fieldset>
            <label className="text-sm">
              Expires in
              <select className="field mt-1.5" value={credentialExpiryDays} onChange={(event) => setCredentialExpiryDays(Number(event.target.value))}>
                {[7, 30, 90, 180, 365].map((days) => <option key={days} value={days}>{days} days</option>)}
              </select>
            </label>
            <div className="sm:col-span-2">
              <button type="submit" className="btn btn-lime" disabled={creatingCredential || !credentialName.trim() || credentialScopes.length === 0}>
                {creatingCredential ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}
                Create credential
              </button>
            </div>
          </form>
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
            <Empty title="No credentials" desc="This node has no configured or runtime-managed credentials." />
          ) : (
            <div className="mt-4 overflow-x-auto">
              <table className="w-full min-w-[800px] text-sm">
                <thead className="border-b border-line text-left text-xs text-faint">
                  <tr>
                    <th scope="col" className="py-2 pr-4 font-normal">Name</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Fingerprint</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Scopes</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Source</th>
                    <th scope="col" className="py-2 pr-4 font-normal">Expires</th>
                    <th scope="col" className="py-2 font-normal">Status</th>
                    <th scope="col" className="py-2 font-normal">Action</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {creds.map((c) => (
                    <tr key={c.id}>
                      <td className="py-2.5 pr-4">{c.name || "—"}</td>
                      <td className="py-2.5 pr-4 font-mono">
                        {c.id}
                        {c.current && <span className="ml-2 rounded bg-lime/15 px-1.5 py-0.5 font-sans text-[0.68rem] text-lime">you</span>}
                      </td>
                      <td className="py-2.5 pr-4 font-mono">{c.scopes.join(", ")}</td>
                      <td className="py-2.5 pr-4 text-muted">{c.source === "static" ? "ATLAS_API_TOKENS" : c.source === "scoped" ? "ATLAS_API_CREDENTIALS" : "Runtime"}</td>
                      <td className="py-2.5 pr-4 text-muted">{c.expires_at ? timestamp(c.expires_at) : "never"}</td>
                      <td className="py-2.5">{c.revoked ? <StatusBadge tone="bad">revoked</StatusBadge> : c.expired ? <StatusBadge tone="bad">expired</StatusBadge> : <StatusBadge tone="ok">active</StatusBadge>}</td>
                      <td className="py-2.5">
                        {c.source === "managed" && !c.revoked && !c.current && (
                          <button type="button" className="btn btn-danger btn-sm" onClick={() => setRevokeCredential(c)}>Revoke</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <section className="panel p-5 xl:col-span-2" aria-labelledby="audit-title">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <ScrollText className="h-5 w-5 text-teal" />
              <h2 id="audit-title" className="mt-3 font-medium">Admin audit log</h2>
              <p className="mt-1 text-sm text-muted">This node&apos;s credential, backup and node-eviction events. Records older than 90 days are pruned.</p>
            </div>
            <button
              type="button"
              className="btn btn-outline"
              disabled={exportingAudit}
              onClick={async () => {
                setExportingAudit(true);
                try {
                  const entries = await client!.allAudit();
                  const blob = new Blob([JSON.stringify({ exported_at: new Date().toISOString(), entries }, null, 2)], { type: "application/json" });
                  downloadBlob(blob, `atlas-admin-audit-${conn!.info.node.id}.json`);
                  toast("ok", `Exported ${entries.length} audit entries.`);
                } catch (err) {
                  toast("error", explain(err));
                } finally {
                  setExportingAudit(false);
                }
              }}
            >
              {exportingAudit ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />} Export JSON
            </button>
          </div>
          {auditError ? <div className="mt-4"><Notice tone="error">{auditError}</Notice></div> : audit === null ? (
            <div className="mt-4"><Skeleton className="h-12" /></div>
          ) : audit.length === 0 ? (
            <Empty title="No admin events yet" desc="Credential changes, backups and node evictions will appear here." />
          ) : (
            <>
              <div className="mt-4 overflow-x-auto">
                <table className="w-full min-w-[700px] text-sm">
                  <thead className="border-b border-line text-left text-xs text-faint">
                    <tr>
                      <th scope="col" className="py-2 pr-4 font-normal">Time</th>
                      <th scope="col" className="py-2 pr-4 font-normal">Action</th>
                      <th scope="col" className="py-2 pr-4 font-normal">Actor</th>
                      <th scope="col" className="py-2 pr-4 font-normal">Resource</th>
                      <th scope="col" className="py-2 font-normal">Details</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {audit.map((entry) => (
                      <tr key={entry.seq}>
                        <td className="whitespace-nowrap py-2.5 pr-4 text-muted">{timestamp(entry.at)}</td>
                        <td className="py-2.5 pr-4 font-mono">{entry.action}</td>
                        <td className="py-2.5 pr-4 font-mono">{entry.actor_id}</td>
                        <td className="py-2.5 pr-4 font-mono">{entry.resource_id || "—"}</td>
                        <td className="max-w-[360px] truncate py-2.5 font-mono text-xs text-muted">{JSON.stringify(entry.details)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {auditBefore && (
                <button
                  type="button"
                  className="btn btn-outline mt-4"
                  disabled={loadingAuditMore}
                  onClick={async () => {
                    setLoadingAuditMore(true);
                    try {
                      const page = await client!.audit(auditBefore, 50);
                      setAudit((current) => [...(current ?? []), ...page.entries]);
                      setAuditBefore(page.next_before);
                    } catch (err) {
                      toast("error", explain(err));
                    } finally {
                      setLoadingAuditMore(false);
                    }
                  }}
                >
                  {loadingAuditMore && <Loader2 className="h-4 w-4 animate-spin" />} Load older events
                </button>
              )}
            </>
          )}
        </section>

        <section className="panel p-5" aria-labelledby="backup-title">
          <DatabaseBackup className="h-5 w-5 text-teal" />
          <h2 id="backup-title" className="mt-3 font-medium">
            Backup
          </h2>
          <p className="mt-1 text-sm text-muted">
            Download a consistent SQLite snapshot of this node&apos;s registry ({services.length} instances), managed credential digests, and local audit history. Store it securely.
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
        open={revokeCredential !== null}
        onOpenChange={(open) => !open && setRevokeCredential(null)}
        title="Revoke this credential?"
        confirmLabel="Revoke credential"
        typeToConfirm={revokeCredential?.id}
        onConfirm={async () => {
          if (!revokeCredential) return;
          try {
            await client!.revokeCredential(revokeCredential.id);
            toast("ok", `Revoked credential ${revokeCredential.id}.`);
            setRevokeCredential(null);
            setCreds(await client!.credentials());
            const page = await client!.audit(undefined, 50);
            setAudit(page.entries);
            setAuditBefore(page.next_before);
          } catch (err) {
            toast("error", explain(err));
          }
        }}
      >
        <p>This credential will stop authenticating as revocation converges across cluster peers. It cannot be restored.</p>
      </ConfirmDialog>

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
