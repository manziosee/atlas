"use client";

import { useCallback, useEffect, useState } from "react";
import { Loader2, Send, Webhook as WebhookIcon } from "lucide-react";
import { explain, type AtlasClient, type Webhook, type WebhookKind } from "@/lib/api";
import { timestamp } from "@/lib/format";
import { CodeBlock } from "@/components/copy-button";
import { ConfirmDialog, Empty, Notice, Skeleton, StatusBadge } from "./ui";

/** Checks a webhook URL the way the node does, before sending it. Exported for tests. */
export function webhookUrlProblem(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return "Enter a full URL, such as https://hooks.example.com/atlas.";
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return "Use an http:// or https:// URL.";
  if (url.username || url.password) return "Leave credentials out of the URL; verify deliveries with the signing secret instead.";
  if (url.hash) return "Remove the #fragment.";
  if (/^169\.254\.|^\[fe80:/i.test(url.host)) return "Link-local addresses, such as cloud metadata endpoints, are refused.";
  return null;
}

function health(hook: Webhook): { tone: "ok" | "bad" | "muted"; label: string } {
  const { last_success_at: ok, last_failure_at: failed } = hook.status;
  if (failed && (!ok || failed > ok)) return { tone: "bad", label: "failing" };
  if (ok) return { tone: "ok", label: "delivering" };
  return { tone: "muted", label: "no events yet" };
}

/** Lists, creates, tests and deletes the connected node's webhooks. */
export function WebhooksPanel({ client, toast }: { client: AtlasClient; toast: (tone: "ok" | "error", message: string) => void }) {
  const [hooks, setHooks] = useState<Webhook[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [url, setUrl] = useState("");
  const [kinds, setKinds] = useState<WebhookKind[]>(["service"]);
  const [service, setService] = useState("");
  const [description, setDescription] = useState("");
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [remove, setRemove] = useState<Webhook | null>(null);
  const urlProblem = url ? webhookUrlProblem(url) : null;

  const load = useCallback(() => {
    client.webhooks().then(setHooks, (err) => setLoadError(explain(err)));
  }, [client]);
  useEffect(load, [load]);

  return (
    <section className="panel p-5 xl:col-span-2" aria-labelledby="webhooks-title">
      <h2 id="webhooks-title" className="flex items-center gap-2 font-medium">
        <WebhookIcon className="h-4 w-4" /> Webhooks
      </h2>
      <p className="mt-1 text-sm text-muted">
        This node POSTs each matching event as JSON, signed with <span className="font-mono">X-Atlas-Signature</span>. Webhooks belong to the node you&apos;re connected to: it is the only node that delivers them. Failed deliveries are retried with backoff.
      </p>
      {secret && (
        <div className="mt-4 rounded-xl border border-warn/40 bg-warn/10 p-4">
          <p className="font-medium text-warn">Copy this signing secret now. It cannot be shown again.</p>
          <div className="mt-3"><CodeBlock title="webhook secret" code={secret} /></div>
          <button type="button" className="btn btn-outline mt-3" onClick={() => setSecret(null)}>Dismiss secret</button>
        </div>
      )}
      <form
        className="mt-5 grid gap-3 rounded-xl border border-line p-4 sm:grid-cols-2"
        onSubmit={async (event) => {
          event.preventDefault();
          if (urlProblem || !url.trim()) return;
          setCreating(true);
          try {
            const created = await client.createWebhook({
              url: url.trim(),
              kinds: kinds.length === 2 ? undefined : kinds,
              service: service.trim() || undefined,
              description: description.trim() || undefined,
            });
            setSecret(created.secret);
            setUrl("");
            setService("");
            setDescription("");
            toast("ok", "Webhook created. Copy its signing secret now.");
            load();
          } catch (err) {
            toast("error", explain(err));
          } finally {
            setCreating(false);
          }
        }}
      >
        <label className="text-sm sm:col-span-2">
          Endpoint URL
          <input className="field mt-1.5 font-mono" value={url} required onChange={(e) => setUrl(e.target.value)} placeholder="https://hooks.example.com/atlas" />
          {urlProblem && <span className="mt-1 block text-xs text-bad">{urlProblem}</span>}
        </label>
        <fieldset className="text-sm">
          <legend className="mb-2">Events</legend>
          <div className="flex flex-wrap gap-4">
            {(["service", "member"] as WebhookKind[]).map((kind) => (
              <label key={kind} className="flex items-center gap-2 text-muted">
                <input type="checkbox" checked={kinds.includes(kind)} onChange={(e) => setKinds((k) => (e.target.checked ? [...k, kind] : k.filter((v) => v !== kind)))} />
                {kind === "service" ? "service changes" : "membership"}
              </label>
            ))}
          </div>
        </fieldset>
        <label className="text-sm">
          Only this service <span className="text-faint">(optional)</span>
          <input className="field mt-1.5 font-mono" value={service} onChange={(e) => setService(e.target.value)} placeholder="payments" />
        </label>
        <label className="text-sm sm:col-span-2">
          Description <span className="text-faint">(optional)</span>
          <input className="field mt-1.5" value={description} maxLength={256} onChange={(e) => setDescription(e.target.value)} placeholder="e.g. deploy notifications" />
        </label>
        <div className="sm:col-span-2">
          <button type="submit" className="btn btn-lime" disabled={creating || !url.trim() || !!urlProblem || kinds.length === 0}>
            {creating ? <Loader2 className="h-4 w-4 animate-spin" /> : <WebhookIcon className="h-4 w-4" />}
            Add webhook
          </button>
        </div>
      </form>

      {loadError ? (
        <div className="mt-4"><Notice tone="error">{loadError}</Notice></div>
      ) : hooks === null ? (
        <div className="mt-4 space-y-2"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
      ) : hooks.length === 0 ? (
        <Empty title="No webhooks" desc="Add an endpoint above to receive service and membership changes as they happen." />
      ) : (
        <div className="mt-4 overflow-x-auto">
          <table className="w-full min-w-[820px] text-sm">
            <thead className="border-b border-line text-left text-xs text-faint">
              <tr>
                <th scope="col" className="py-2 pr-4 font-normal">Endpoint</th>
                <th scope="col" className="py-2 pr-4 font-normal">Events</th>
                <th scope="col" className="py-2 pr-4 font-normal">Delivered</th>
                <th scope="col" className="py-2 pr-4 font-normal">Failed / dropped</th>
                <th scope="col" className="py-2 pr-4 font-normal">Status</th>
                <th scope="col" className="py-2 font-normal">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {hooks.map((hook) => {
                const state = health(hook);
                return (
                  <tr key={hook.id}>
                    <td className="max-w-[320px] py-2.5 pr-4">
                      <div className="truncate font-mono" title={hook.url}>{hook.url}</div>
                      <div className="text-xs text-faint">{hook.description || hook.id} · added {timestamp(hook.created_at)}</div>
                    </td>
                    <td className="py-2.5 pr-4 text-muted">
                      {(hook.kinds?.length ? hook.kinds : ["service", "member"]).join(", ")}
                      {hook.service && <span className="font-mono"> · {hook.service}</span>}
                    </td>
                    <td className="py-2.5 pr-4 font-mono">{hook.status.delivered}</td>
                    <td className="py-2.5 pr-4 font-mono">{hook.status.failed} / {hook.status.dropped}</td>
                    <td className="py-2.5 pr-4">
                      <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
                      {state.tone === "bad" && hook.status.last_error && <div className="mt-1 max-w-[240px] truncate text-xs text-muted" title={hook.status.last_error}>{hook.status.last_error}</div>}
                    </td>
                    <td className="py-2.5">
                      <div className="flex gap-2">
                        <button
                          type="button"
                          className="btn btn-outline btn-sm"
                          disabled={testing === hook.id}
                          onClick={async () => {
                            setTesting(hook.id);
                            try {
                              const result = await client.testWebhook(hook.id);
                              toast(result.delivered ? "ok" : "error", result.delivered ? `Test event delivered (HTTP ${result.result.status_code}, ${result.result.duration_ms} ms).` : `Test failed: ${result.result.error}`);
                            } catch (err) {
                              toast("error", explain(err));
                            } finally {
                              setTesting(null);
                            }
                          }}
                        >
                          {testing === hook.id ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Send className="h-3.5 w-3.5" />} Test
                        </button>
                        <button type="button" className="btn btn-danger btn-sm" onClick={() => setRemove(hook)}>Delete</button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={remove !== null}
        onOpenChange={(open) => !open && setRemove(null)}
        title="Delete this webhook?"
        confirmLabel="Delete webhook"
        onConfirm={async () => {
          if (!remove) return;
          try {
            await client.deleteWebhook(remove.id);
            toast("ok", "Webhook deleted.");
            setRemove(null);
            load();
          } catch (err) {
            toast("error", explain(err));
          }
        }}
      >
        <p>{remove?.url} will stop receiving events from this node. Pending deliveries are discarded.</p>
      </ConfirmDialog>
    </section>
  );
}
