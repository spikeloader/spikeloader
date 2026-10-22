"use client";

import Link from "next/link";
import { useEffect, useRef, useState, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Icon, type IconName } from "./icons";

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

/* ---------- buttons ---------- */

type Variant = "primary" | "secondary" | "ghost" | "danger" | "danger-solid";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-accent text-accent-fg shadow-sm hover:opacity-90",
  secondary: "bg-surface text-fg border border-border hover:border-muted hover:bg-surface-2",
  ghost: "text-muted hover:text-fg hover:bg-surface-2",
  danger: "text-danger border border-border bg-surface hover:border-danger hover:bg-danger/10",
  "danger-solid": "bg-danger text-danger-fg hover:opacity-90",
};

export function Button({
  variant = "secondary",
  size = "md",
  icon,
  className,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md"; icon?: IconName }) {
  return (
    <button
      {...props}
      className={cx(
        "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-lg font-medium whitespace-nowrap transition focus-visible:ring-2 focus-visible:ring-accent/50 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-40",
        size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm",
        VARIANTS[variant],
        className,
      )}
    >
      {icon && <Icon name={icon} className={size === "sm" ? "size-3.5" : "size-4"} />}
      {children}
    </button>
  );
}

export function ButtonLink({ href, variant = "secondary", size = "md", icon, children, className }: { href: string; variant?: Variant; size?: "sm" | "md"; icon?: IconName; children: ReactNode; className?: string }) {
  return (
    <Link
      href={href}
      className={cx("inline-flex shrink-0 items-center justify-center gap-1.5 rounded-lg font-medium whitespace-nowrap transition", size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm", VARIANTS[variant], className)}
    >
      {icon && <Icon name={icon} className={size === "sm" ? "size-3.5" : "size-4"} />}
      {children}
    </Link>
  );
}

/* ---------- layout ---------- */

export function PageHeader({
  back,
  title,
  badge,
  description,
  meta,
  actions,
}: {
  back?: { href: string; label: string };
  title: ReactNode;
  badge?: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3">
      {back && (
        <Link href={back.href} className="inline-flex w-fit items-center gap-1.5 text-sm text-muted hover:text-fg">
          <Icon name="back" className="size-3.5" />
          {back.label}
        </Link>
      )}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight">{title}</h1>
            {badge}
          </div>
          {description && <div className="mt-1 max-w-3xl text-sm text-muted">{description}</div>}
          {meta && <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted">{meta}</div>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}

/** Title of a page section (below the PageHeader), with optional badge and meta line. */
export function SectionTitle({ icon, title, badge, meta }: { icon: IconName; title: ReactNode; badge?: ReactNode; meta?: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="flex items-center gap-2 text-lg font-semibold tracking-tight">
          <Icon name={icon} className="size-5 text-muted" />
          {title}
        </h2>
        {badge}
      </div>
      {meta && <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-xs text-muted">{meta}</div>}
    </div>
  );
}

export function MetaItem({ icon, children, title }: { icon: IconName; children: ReactNode; title?: string }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5" title={title}>
      <Icon name={icon} className="size-3.5 shrink-0" />
      <span className="truncate">{children}</span>
    </span>
  );
}

export function Card({
  title,
  description,
  icon,
  actions,
  children,
  className,
  flush = false,
}: {
  title?: ReactNode;
  description?: ReactNode;
  icon?: IconName;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  /** No inner padding (tables, lists that touch the edges). */
  flush?: boolean;
}) {
  return (
    <section className={cx("min-w-0 rounded-xl border border-border bg-surface shadow-sm", className)}>
      {(title || actions) && (
        <header className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <h2 className="flex items-center gap-2 text-sm font-semibold">
              {icon && <Icon name={icon} className="size-4 text-muted" />}
              {title}
            </h2>
            {description && <p className="mt-0.5 text-xs text-muted">{description}</p>}
          </div>
          {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={flush ? "" : "p-4"}>{children}</div>
    </section>
  );
}

/** Route tabs (sub-navigation). */
export function LinkTabs({ items, active }: { items: { href: string; label: string; icon?: IconName; count?: number }[]; active: string }) {
  return (
    <nav className="-mx-4 overflow-x-auto overflow-y-hidden border-b border-border px-4 sm:mx-0 sm:px-0">
      <div className="flex min-w-max gap-1">
        {items.map((t) => (
          <Link
            key={t.href}
            href={t.href}
            className={cx(
              "-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-2.5 text-sm transition",
              t.href === active ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg",
            )}
          >
            {t.icon && <Icon name={t.icon} className="size-4" />}
            {t.label}
            {t.count !== undefined && <span className="rounded-full bg-surface-2 px-1.5 text-[11px] text-muted tabular-nums">{t.count}</span>}
          </Link>
        ))}
      </div>
    </nav>
  );
}

/** In-page tabs (state owned by the caller). */
export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string; icon?: IconName; count?: number }[]; value: T; onChange: (id: T) => void }) {
  return (
    <div className="-mx-4 overflow-x-auto overflow-y-hidden border-b border-border px-4 sm:mx-0 sm:px-0" role="tablist">
      <div className="flex min-w-max gap-1">
        {tabs.map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={t.id === value}
            onClick={() => onChange(t.id)}
            className={cx(
              "-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-2.5 text-sm transition",
              t.id === value ? "border-accent font-medium text-fg" : "border-transparent text-muted hover:text-fg",
            )}
          >
            {t.icon && <Icon name={t.icon} className="size-4" />}
            {t.label}
            {t.count !== undefined && <span className="rounded-full bg-surface-2 px-1.5 text-[11px] text-muted tabular-nums">{t.count}</span>}
          </button>
        ))}
      </div>
    </div>
  );
}

/* ---------- status ---------- */

type Tone = "ok" | "danger" | "warn" | "accent" | "neutral";

const TONE: Record<string, Tone> = {
  running: "ok",
  ok: "ok",
  success: "ok",
  online: "ok",
  done: "ok",
  failed: "danger",
  error: "danger",
  paused: "warn",
  manual: "warn",
  waiting: "warn",
  starting: "accent",
  queued: "neutral",
  pending: "neutral",
  skipped: "neutral",
  canceled: "neutral",
  stopped: "neutral",
  off: "neutral",
  offline: "neutral",
};

const CHIP: Record<Tone, string> = {
  ok: "border-ok/30 bg-ok/10 text-ok",
  danger: "border-danger/30 bg-danger/10 text-danger",
  warn: "border-warn/30 bg-warn/10 text-warn",
  accent: "border-accent/30 bg-accent/10 text-accent",
  neutral: "border-border bg-surface-2 text-muted",
};

const DOT: Record<Tone, string> = { ok: "bg-ok", danger: "bg-danger", warn: "bg-warn", accent: "bg-accent", neutral: "bg-muted" };

export function StatusBadge({ state, label, tone, pulse }: { state: string; label?: string; tone?: Tone; pulse?: boolean }) {
  const t = tone ?? TONE[state] ?? "neutral";
  return (
    <span className={cx("inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap", CHIP[t])}>
      <span className={cx("size-1.5 rounded-full", DOT[t], (pulse ?? (state === "running" || state === "starting")) && "animate-pulse")} />
      {label ?? state}
    </span>
  );
}

export function Meter({ label, value, max, detail, icon }: { label: string; value: number; max: number; detail: string; icon?: IconName }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const tone = pct > 90 ? "bg-danger" : pct > 70 ? "bg-warn" : "bg-accent";
  return (
    <div>
      <div className="mb-1.5 flex items-baseline justify-between gap-2 text-xs">
        <span className="inline-flex items-center gap-1.5 text-muted">
          {icon && <Icon name={icon} className="size-3.5" />}
          {label}
        </span>
        <span className="font-mono tabular-nums">{detail}</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-surface-2">
        <div className={cx("h-full rounded-full transition-all", tone)} style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

export function Stat({ label, value, hint, icon, tone }: { label: string; value: ReactNode; hint?: ReactNode; icon?: IconName; tone?: "ok" | "danger" | "warn" }) {
  return (
    <div className="rounded-xl border border-border bg-surface px-4 py-3 shadow-sm">
      <div className="flex items-center justify-between gap-2 text-xs text-muted">
        {label}
        {icon && <Icon name={icon} className="size-4" />}
      </div>
      <div className={cx("mt-1 font-mono text-2xl font-semibold tabular-nums", tone && { ok: "text-ok", danger: "text-danger", warn: "text-warn" }[tone])}>{value}</div>
      {hint && <div className="mt-0.5 truncate text-xs text-muted">{hint}</div>}
    </div>
  );
}

/* ---------- forms and data ---------- */

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-1.5 text-xs font-medium text-muted">
      {label}
      {children}
      {hint && <span className="font-normal">{hint}</span>}
    </label>
  );
}

export const inputClass =
  "h-9 w-full min-w-0 rounded-lg border border-border bg-bg px-3 font-mono text-sm text-fg outline-none transition placeholder:text-muted/60 focus:border-accent focus:ring-2 focus:ring-accent/20";

/** Label/value rows. */
export function KV({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="truncate text-right font-mono">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function CopyField({ value, secret = false }: { value: string; secret?: boolean }) {
  const [copied, setCopied] = useState(false);
  const [shown, setShown] = useState(!secret);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <div className="flex items-stretch overflow-hidden rounded-lg border border-border bg-bg">
      <code className="min-w-0 flex-1 truncate px-3 py-2 font-mono text-xs" title={shown ? value : undefined}>
        {shown ? value : value.replace(/token=.*/, "token=••••••••")}
      </code>
      {secret && (
        <button className="border-l border-border px-2.5 text-xs text-muted hover:text-fg" onClick={() => setShown(!shown)}>
          {shown ? "hide" : "show"}
        </button>
      )}
      <button
        className="inline-flex items-center gap-1 border-l border-border px-2.5 text-xs text-muted hover:text-fg"
        onClick={() => navigator.clipboard.writeText(value).then(() => setCopied(true))}
      >
        <Icon name={copied ? "check" : "copy"} className="size-3.5" />
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}

export function Empty({ icon = "info", title, children, action }: { icon?: IconName; title?: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-10 text-center">
      <span className="grid size-10 place-items-center rounded-full bg-surface-2 text-muted">
        <Icon name={icon} className="size-5" />
      </span>
      {title && <div className="text-sm font-medium">{title}</div>}
      {children && <div className="max-w-md text-sm text-muted">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function Notice({ tone = "info", children }: { tone?: "info" | "danger" | "warn"; children: ReactNode }) {
  return (
    <div
      className={cx(
        "flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-sm",
        tone === "danger" ? "border-danger/30 bg-danger/5 text-danger" : tone === "warn" ? "border-warn/30 bg-warn/5 text-fg" : "border-border bg-surface-2/60 text-muted",
      )}
    >
      <Icon name={tone === "info" ? "info" : "alert"} className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

export function Modal({ title, open, onClose, children }: { title: string; open: boolean; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (open) ref.current?.showModal();
    else ref.current?.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => e.target === ref.current && onClose()}
      className="m-auto w-[min(32rem,calc(100vw-2rem))] rounded-xl border border-border bg-surface p-0 text-fg shadow-2xl backdrop:bg-black/50 backdrop:backdrop-blur-[2px] open:animate-[confirm-in_120ms_ease-out]"
    >
      <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
        <h2 className="text-base font-semibold">{title}</h2>
        <button onClick={onClose} className="text-muted hover:text-fg" aria-label="close">
          <Icon name="x" />
        </button>
      </div>
      <div className="p-5">{open && children}</div>
    </dialog>
  );
}

/* ---------- formatting ---------- */

export function fmtBytes(n: number) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
}

export function fmtDuration(s: number) {
  if (!s) return "—";
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
