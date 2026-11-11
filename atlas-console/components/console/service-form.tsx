"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
import { explain, type Service, type ServicePatch, type ServiceSpec } from "@/lib/api";
import { Field, Notice } from "./ui";

type HealthMode = "ttl" | "check" | "none";

const NAME = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;

export function healthMode(s?: Pick<Service, "ttl_seconds" | "health_check">): HealthMode {
  if (s?.ttl_seconds) return "ttl";
  if (s?.health_check) return "check";
  return "none";
}

/** Turns form values into a spec, or explains what to fix. Exported for tests. */
export function buildSpec(v: FormValues): { spec?: ServiceSpec; errors: Partial<Record<keyof FormValues, string>> } {
  const errors: Partial<Record<keyof FormValues, string>> = {};
  if (!NAME.test(v.name)) errors.name = "1–128 letters, digits, dots, underscores or hyphens, starting with a letter or digit.";
  if (v.id && !NAME.test(v.id)) errors.id = "Same rules as the name, or leave it empty for a random ID.";
  const port = /:(\d+)$/.exec(v.address.trim());
  if (!port || +port[1] < 1 || +port[1] > 65535 || v.address.trim().startsWith(":")) errors.address = "Use host:port, for example 10.0.0.8:8080 or [fd00::1]:8080.";
  const tags = v.tags.split(",").map((t) => t.trim()).filter(Boolean);
  if (tags.length > 16) errors.tags = "At most 16 tags.";
  if (tags.some((t) => t.length > 64)) errors.tags = "Each tag can be at most 64 characters.";
  const trafficWeight = Number(v.trafficWeight);
  if (!Number.isInteger(trafficWeight) || trafficWeight < 1 || trafficWeight > 1000) errors.trafficWeight = "Whole number between 1 and 1000.";
  const spec: ServiceSpec = { name: v.name.trim(), address: v.address.trim(), version: v.version.trim() || undefined, tags, draining: v.draining, traffic_weight: trafficWeight };
  if (v.id.trim()) spec.id = v.id.trim();
  if (v.mode === "ttl") {
    const ttl = Number(v.ttl);
    if (!Number.isInteger(ttl) || ttl < 1 || ttl > 86400) errors.ttl = "Whole seconds between 1 and 86400.";
    spec.ttl_seconds = ttl;
  } else if (v.mode === "check") {
    if (!/^(https?|tcp|grpcs?):\/\//.test(v.check)) errors.check = "Start with http://, https://, tcp://, grpc:// or grpcs://, on the service address.";
    spec.health_check = v.check.trim();
    if (v.interval) {
      const n = Number(v.interval);
      if (!Number.isInteger(n) || n < 1 || n > 3600) errors.interval = "Whole seconds between 1 and 3600.";
      spec.check_interval_seconds = n;
    }
  }
  return { spec: Object.keys(errors).length ? undefined : spec, errors };
}

export interface FormValues {
  id: string;
  name: string;
  address: string;
  version: string;
  tags: string;
  mode: HealthMode;
  ttl: string;
  check: string;
  interval: string;
  draining: boolean;
  trafficWeight: string;
}

function initial(s?: Service): FormValues {
  return {
    id: s?.id ?? "",
    name: s?.name ?? "",
    address: s?.address ?? "",
    version: s?.version ?? "",
    tags: (s?.tags ?? []).join(", "),
    mode: s ? healthMode(s) : "check",
    ttl: String(s?.ttl_seconds ?? 15),
    check: s?.health_check ?? "",
    interval: s?.check_interval_seconds ? String(s.check_interval_seconds) : "",
    draining: !!s?.draining,
    trafficWeight: String(s?.traffic_weight ?? 1),
  };
}

/** Register a new service, or edit an existing one with an If-Match revision. */
export function ServiceForm({
  service,
  onSubmit,
  onCancel,
}: {
  service?: Service;
  onSubmit: (spec: ServiceSpec, patch: ServicePatch) => Promise<void>;
  onCancel: () => void;
}) {
  const [v, setV] = useState<FormValues>(() => initial(service));
  const [errors, setErrors] = useState<Partial<Record<keyof FormValues, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof FormValues>(k: K, value: FormValues[K]) => setV((x) => ({ ...x, [k]: value }));
  const editing = !!service;

  return (
    <form
      className="space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        const { spec, errors } = buildSpec(v);
        setErrors(errors);
        if (!spec) return;
        // A patch clears the health setting the user switched away from.
        const patch: ServicePatch = {
          name: spec.name,
          address: spec.address,
          version: spec.version ?? "",
          tags: spec.tags,
          health_check: spec.health_check ?? "",
          ttl_seconds: spec.ttl_seconds ?? 0,
          // 0 clears a setting; check tuning is only valid with a health check.
          check_interval_seconds: spec.health_check ? (spec.check_interval_seconds ?? 0) : 0,
          ...(spec.health_check ? {} : { unhealthy_threshold: 0, healthy_threshold: 0 }),
          draining: spec.draining,
          traffic_weight: spec.traffic_weight,
        };
        setBusy(true);
        setFailure(null);
        try {
          await onSubmit(spec, patch);
        } catch (err) {
          setFailure(explain(err));
        } finally {
          setBusy(false);
        }
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name" error={errors.name}>
          <input className="field font-mono" value={v.name} onChange={(e) => set("name", e.target.value)} placeholder="payments" required />
        </Field>
        <Field label="Address" error={errors.address} hint="host:port the service listens on">
          <input className="field font-mono" value={v.address} onChange={(e) => set("address", e.target.value)} placeholder="10.0.0.8:8080" required />
        </Field>
        <Field label="Version" hint="optional">
          <input className="field font-mono" value={v.version} onChange={(e) => set("version", e.target.value)} placeholder="1.4.2" />
        </Field>
        <Field label="Tags" error={errors.tags} hint="comma-separated">
          <input className="field font-mono" value={v.tags} onChange={(e) => set("tags", e.target.value)} placeholder="primary, grpc" />
        </Field>
        {!editing && (
          <Field label="ID" error={errors.id} hint="optional; a stable ID makes retries safe">
            <input className="field font-mono" value={v.id} onChange={(e) => set("id", e.target.value)} placeholder="payments-eu-1" />
          </Field>
        )}
      </div>

      <Field label="Traffic weight" error={errors.trafficWeight} hint="Weighted within the healthy, regional and latency-preferred pool. Set the sum per version to the ratio you want (stable 95, canary 5); unhealthy instances are excluded automatically.">
        <input className="field font-mono" type="number" min={1} max={1000} step={1} inputMode="numeric" value={v.trafficWeight} onChange={(e) => set("trafficWeight", e.target.value)} />
      </Field>

      <fieldset>
        <legend className="text-sm font-medium">Health</legend>
        <div className="mt-2 grid gap-2 sm:grid-cols-3">
          {(
            [
              ["check", "Health check", "The owning node probes it"],
              ["ttl", "TTL heartbeat", "Your service reports in"],
              ["none", "None", "Health stays unknown"],
            ] as const
          ).map(([mode, label, help]) => (
            <label key={mode} className={`cursor-pointer rounded-xl border p-3 text-sm ${v.mode === mode ? "border-lime/50 bg-lime/[0.06]" : "border-line-strong"}`}>
              <input type="radio" name="mode" className="sr-only" checked={v.mode === mode} onChange={() => set("mode", mode)} />
              <span className="font-medium">{label}</span>
              <span className="mt-0.5 block text-xs text-muted">{help}</span>
            </label>
          ))}
        </div>
      </fieldset>
      {v.mode === "check" && (
        <div className="grid gap-4 sm:grid-cols-[1fr_9rem]">
          <Field label="Check URL" error={errors.check} hint="http(s)://, tcp:// or grpc(s):// on the address above">
            <input className="field font-mono" value={v.check} onChange={(e) => set("check", e.target.value)} placeholder={`http://${v.address || "10.0.0.8:8080"}/healthz`} />
          </Field>
          <Field label="Interval (s)" error={errors.interval} hint="node default if empty">
            <input className="field font-mono" inputMode="numeric" value={v.interval} onChange={(e) => set("interval", e.target.value)} placeholder="5" />
          </Field>
        </div>
      )}
      {v.mode === "ttl" && (
        <Field label="TTL (seconds)" error={errors.ttl} hint="Send a heartbeat about every third of this. One missed TTL marks it unhealthy; three remove it.">
          <input className="field font-mono" inputMode="numeric" value={v.ttl} onChange={(e) => set("ttl", e.target.value)} />
        </Field>
      )}
      <label className="flex items-start gap-2.5 text-sm">
        <input type="checkbox" className="mt-1 accent-[var(--color-lime)]" checked={v.draining} onChange={(e) => set("draining", e.target.checked)} />
        <span>
          Draining <span className="block text-xs text-muted">Keep the record but send it no traffic.</span>
        </span>
      </label>
      {editing && (v.address !== service.address || healthMode(service) !== v.mode || v.check !== (service.health_check ?? "")) && (
        <Notice tone="info">Changing the address or health settings resets health to unknown until the next check or heartbeat.</Notice>
      )}
      {failure && <Notice tone="error">{failure}</Notice>}
      <div className="flex justify-end gap-2 pt-2">
        <button type="button" className="btn btn-outline" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className="btn btn-lime" disabled={busy}>
          {busy && <Loader2 className="h-4 w-4 animate-spin" />} {editing ? "Save changes" : "Register service"}
        </button>
      </div>
    </form>
  );
}
