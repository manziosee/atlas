"use client";

import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useState, type ReactNode } from "react";
import { AlertTriangle, CheckCircle2, CircleDashed, CircleSlash, HelpCircle, Inbox, Info, Loader2, X, XCircle } from "lucide-react";
import { cn, type Tone } from "@/lib/format";

const toneClass: Record<Tone, string> = {
  ok: "text-ok border-ok/35 bg-ok/10",
  warn: "text-warn border-warn/35 bg-warn/10",
  bad: "text-bad border-bad/40 bg-bad/10",
  muted: "text-muted border-line-strong bg-white/[0.03]",
  info: "text-info border-info/35 bg-info/10",
};
const toneIcon: Record<Tone, typeof CheckCircle2> = {
  ok: CheckCircle2,
  warn: HelpCircle,
  bad: XCircle,
  muted: CircleSlash,
  info: CircleDashed,
};

/** Status shown with text and an icon as well as colour. */
export function StatusBadge({ tone, children, className }: { tone: Tone; children: ReactNode; className?: string }) {
  const Icon = toneIcon[tone];
  return (
    <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs font-medium", toneClass[tone], className)}>
      <Icon className="h-3.5 w-3.5" aria-hidden />
      {children}
    </span>
  );
}

export function Tag({ children }: { children: ReactNode }) {
  return <span className="rounded-md border border-line-strong bg-white/[0.03] px-1.5 py-0.5 font-mono text-[0.7rem] text-muted">{children}</span>;
}

export function PageHeader({ title, desc, children }: { title: string; desc: ReactNode; children?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted">{desc}</p>
      </div>
      {children && <div className="flex flex-wrap gap-2">{children}</div>}
    </div>
  );
}

export function Empty({ title, desc, children }: { title: string; desc: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-14 text-center">
      <Inbox className="h-8 w-8 text-faint" aria-hidden />
      <p className="font-medium">{title}</p>
      <p className="max-w-sm text-sm text-muted">{desc}</p>
      {children}
    </div>
  );
}

export function Notice({ tone = "warn", title, children }: { tone?: "warn" | "error" | "info" | "ok"; title?: string; children: ReactNode }) {
  const map = {
    warn: ["border-warn/35 bg-warn/10 text-warn", AlertTriangle],
    error: ["border-bad/40 bg-bad/10 text-bad", XCircle],
    info: ["border-info/35 bg-info/10 text-info", Info],
    ok: ["border-ok/35 bg-ok/10 text-ok", CheckCircle2],
  } as const;
  const [cls, Icon] = map[tone];
  return (
    <div role={tone === "error" ? "alert" : "status"} className={cn("flex items-start gap-2.5 rounded-xl border px-3.5 py-3 text-sm", cls)}>
      <Icon className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
      <div className="min-w-0 text-fg/90">
        {title && <p className="font-medium text-fg">{title}</p>}
        <div className={title ? "mt-0.5 text-fg/80" : ""}>{children}</div>
      </div>
    </div>
  );
}

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <span className="inline-flex items-center gap-2 text-sm text-muted" role="status">
      <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> {label}…
    </span>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-lg bg-white/[0.06]", className)} aria-hidden />;
}

export function Stat({ label, value, hint, tone }: { label: string; value: ReactNode; hint?: ReactNode; tone?: Tone }) {
  return (
    <div className="panel p-5">
      <p className="text-xs uppercase tracking-wider text-faint">{label}</p>
      <p className={cn("mt-2 font-mono text-3xl font-semibold tabular-nums", tone === "warn" && "text-warn", tone === "bad" && "text-bad", tone === "ok" && "text-ok")}>{value}</p>
      {hint && <div className="mt-1.5 text-xs text-muted">{hint}</div>}
    </div>
  );
}

export function SearchInput({ value, onChange, placeholder, label }: { value: string; onChange: (v: string) => void; placeholder: string; label: string }) {
  return (
    <label className="relative block min-w-[220px] flex-1">
      <span className="sr-only">{label}</span>
      <input type="search" className="field pl-3" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

export function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn("rounded-full border px-3 py-1 text-xs transition-colors", active ? "border-lime/50 bg-lime/10 text-lime" : "border-line-strong text-muted hover:text-fg")}
    >
      {children}
    </button>
  );
}

/** Accessible modal (focus trap, Escape, scroll lock) built on Radix Dialog. */
export function Modal({
  open,
  onOpenChange,
  title,
  description,
  children,
  side = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  side?: boolean;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm" />
        <Dialog.Content
          className={cn(
            "fixed z-50 overflow-y-auto border border-line-strong bg-panel shadow-2xl focus:outline-none",
            side
              ? "inset-y-0 right-0 w-full max-w-xl p-6 sm:rounded-l-2xl"
              : "left-1/2 top-1/2 max-h-[90dvh] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 rounded-2xl p-6",
          )}
        >
          <div className="mb-4 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <Dialog.Title className="text-lg font-semibold">{title}</Dialog.Title>
              {description ? <Dialog.Description className="mt-1 text-sm text-muted">{description}</Dialog.Description> : <Dialog.Description className="sr-only">{title}</Dialog.Description>}
            </div>
            <Dialog.Close className="rounded-lg p-1.5 text-muted hover:bg-white/5 hover:text-fg" aria-label="Close">
              <X className="h-4 w-4" />
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/**
 * Confirmation for destructive actions. With `typeToConfirm`, the user must
 * type that text first.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  children,
  confirmLabel,
  typeToConfirm,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  typeToConfirm?: string;
  onConfirm: () => Promise<void>;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setTyped("");
      setBusy(false);
    }
  }, [open]);
  const blocked = !!typeToConfirm && typed.trim() !== typeToConfirm;
  return (
    <Modal open={open} onOpenChange={(o) => !busy && onOpenChange(o)} title={title}>
      <div className="space-y-4 text-sm text-muted">{children}</div>
      {typeToConfirm && (
        <label className="mt-4 block text-sm">
          Type <span className="font-mono text-fg">{typeToConfirm}</span> to confirm
          <input className="field mt-1.5 font-mono" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
        </label>
      )}
      <div className="mt-6 flex justify-end gap-2">
        <button type="button" className="btn btn-outline" onClick={() => onOpenChange(false)} disabled={busy}>
          Cancel
        </button>
        <button
          type="button"
          className="btn btn-danger"
          disabled={blocked || busy}
          onClick={async () => {
            setBusy(true);
            try {
              await onConfirm();
              onOpenChange(false);
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy && <Loader2 className="h-4 w-4 animate-spin" />} {confirmLabel}
        </button>
      </div>
    </Modal>
  );
}

export function Field({ label, hint, children, error }: { label: string; hint?: ReactNode; children: ReactNode; error?: string }) {
  return (
    <label className="block text-sm">
      <span className="font-medium">{label}</span>
      <div className="mt-1.5">{children}</div>
      {error ? <span className="mt-1 block text-xs text-bad">{error}</span> : hint ? <span className="mt-1 block text-xs text-faint">{hint}</span> : null}
    </label>
  );
}
