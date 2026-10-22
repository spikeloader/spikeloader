"use client";

import Link from "next/link";
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { CiEvent, RunSummary, SecretInfo } from "@/lib/ci/types";
import { useConfirm } from "../confirm";
import { Icon, type IconName } from "../icons";
import { api } from "../providers";
import { Button, Empty, LinkTabs, PageHeader, StatusBadge, cx, fmtDuration, inputClass } from "../ui";

/** Fetches `url` now and every `ms` while the tab is visible. */
export function usePoll<T>(url: string | null, ms = 3000) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    if (!url) return;
    try {
      setData(await api<T>(url));
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [url]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial polling load
    reload();
    const t = setInterval(() => !document.hidden && reload(), ms);
    return () => clearInterval(t);
  }, [reload, ms]);

  return { data, error, reload };
}

/* ---------- section chrome ---------- */

const SECTION = [
  { href: "/ci", label: "Projects", icon: "pipelines" as const },
  { href: "/ci/runs", label: "Runs", icon: "list" as const },
  { href: "/ci/validate", label: "Validate", icon: "file" as const },
  { href: "/ci/library", label: "Images & templates", icon: "box" as const },
  { href: "/ci/secrets", label: "Global secrets", icon: "key" as const },
];

/** Header + sub-navigation shared by the top-level Pipelines pages. */
export function CiSection({ active, actions, children }: { active: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <>
      <div className="flex flex-col gap-4">
        <PageHeader
          title="Pipelines"
          description="CI/CD on throwaway microVMs: every job boots a clean VM, runs and is destroyed."
          actions={actions}
        />
        <LinkTabs items={SECTION} active={active} />
      </div>
      {children}
    </>
  );
}

/* ---------- status & formatting ---------- */

const LABEL: Record<string, string> = { manual: "awaiting approval", waiting: "awaiting approval" };

/** Run/job status chip; a running job uses the accent color (green is for success). */
export function CiStatus({ status }: { status: string }) {
  return <StatusBadge state={status} label={LABEL[status] ?? status} tone={status === "running" ? "accent" : undefined} />;
}

export const STATUS_ICON: Record<string, { icon: IconName; className: string }> = {
  success: { icon: "check", className: "text-ok" },
  failed: { icon: "x", className: "text-danger" },
  running: { icon: "refresh", className: "animate-spin text-accent [animation-duration:2s]" },
  manual: { icon: "user", className: "text-warn" },
  waiting: { icon: "user", className: "text-warn" },
  canceled: { icon: "stop", className: "text-muted" },
  skipped: { icon: "chevron", className: "text-muted" },
  queued: { icon: "clock", className: "text-muted" },
  pending: { icon: "clock", className: "text-muted" },
};

export function StatusIcon({ status, className = "size-4" }: { status: string; className?: string }) {
  const s = STATUS_ICON[status] ?? STATUS_ICON.pending;
  return <Icon name={s.icon} className={cx(className, s.className)} />;
}

export const EVENT_ICON: Record<CiEvent, IconName> = { push: "commit", manual: "play", schedule: "clock", webhook: "link", retry: "refresh" };

export function duration(start: number | null, end: number | null) {
  if (!start) return "—";
  const s = Math.round(((end ?? Date.now()) - start) / 1000);
  return s < 1 ? "<1s" : fmtDuration(s);
}

/** 60 → "1 min", 90 → "1m 30s" */
export function every(s: number) {
  return s % 3600 === 0 ? `${s / 3600} h` : s % 60 === 0 ? `${s / 60} min` : fmtDuration(s);
}

export function ago(ts: number | null) {
  if (!ts) return "never";
  const s = Math.round((Date.now() - ts) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

/** "git@github.com:me/app.git" → "github.com/me/app" */
export function shortRepo(url: string) {
  return url
    .replace(/^[a-z+]+:\/\//, "")
    .replace(/^[^@/]+@/, "")
    .replace(/^([^/:]+):/, "$1/")
    .replace(/\.git$/, "");
}

/** One segment per job, in stage order. */
export function JobBar({ run, className }: { run: RunSummary; className?: string }) {
  return (
    <span className={cx("inline-flex gap-0.5", className)}>
      {run.jobs.map((j) => (
        <span
          key={j.idx}
          title={`${j.name}: ${j.status}`}
          className={cx(
            "h-2 w-3 rounded-sm",
            { success: "bg-ok", failed: "bg-danger", running: "animate-pulse bg-accent", manual: "bg-warn" }[j.status as string] ?? "bg-border",
          )}
        />
      ))}
    </span>
  );
}

/** Runs as clickable rows; collapses to two lines on small screens. */
export function RunsList({ runs, showProject = false, empty }: { runs: RunSummary[]; showProject?: boolean; empty?: ReactNode }) {
  if (!runs.length) return <div className="p-4">{empty ?? <Empty icon="list" title="No runs yet" />}</div>;
  return (
    <ul className="divide-y divide-border">
      {runs.map((r) => (
        <li key={r.id}>
          <Link href={`/ci/runs/${r.id}`} className="flex items-center gap-3 px-4 py-3 transition hover:bg-surface-2/50">
            <StatusIcon status={r.status} className="size-4 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                <span className="font-mono text-xs text-muted">#{r.id}</span>
                <span className="truncate text-sm font-medium">{r.message || "(no commit message)"}</span>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
                {showProject && <span className="font-medium text-fg">{r.project}</span>}
                <span className="inline-flex items-center gap-1">
                  <Icon name="branch" className="size-3" />
                  <span className="max-w-[12rem] truncate font-mono">{r.branch}</span>
                </span>
                <span className="font-mono">{r.sha.slice(0, 7)}</span>
                <span className="inline-flex items-center gap-1">
                  <Icon name={EVENT_ICON[r.event]} className="size-3" />
                  {r.event} · {ago(r.createdAt)}
                </span>
              </div>
            </div>
            <JobBar run={r} className="hidden sm:inline-flex" />
            <div className="hidden w-36 shrink-0 justify-end sm:flex">
              <CiStatus status={r.status} />
            </div>
            <span className="w-14 shrink-0 text-right font-mono text-xs text-muted tabular-nums">{duration(r.startedAt, r.endedAt)}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

/* ---------- secrets ---------- */

/** Write-only secrets of a project or of "_global". */
export function SecretsEditor({ scope, names, onChange }: { scope: string; names: SecretInfo[]; onChange: () => void }) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [protectedSecret, setProtectedSecret] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const confirm = useConfirm();

  const save = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api(`/api/ci/secrets/${scope}`, { method: "PUT", body: { name: name.trim(), value, protected: protectedSecret } });
      setName("");
      setValue("");
      setProtectedSecret(true);
      setError(null);
      onChange();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const remove = async (n: string) => {
    if (!(await confirm({ title: `Delete secret ${n}?`, message: "Jobs that list it in `secrets:` will fail until it is added again.", confirmLabel: "Delete" }))) return;
    try {
      await api(`/api/ci/secrets/${scope}`, { method: "DELETE", body: { name: n } });
      setError(null);
      onChange();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      {names.length ? (
        <ul className="divide-y divide-border rounded-lg border border-border">
          {names.map((secret) => (
            <li key={secret.name} className="flex items-center gap-3 px-3 py-2">
              <Icon name="key" className="size-3.5 text-muted" />
              <span className="min-w-0 flex-1 truncate font-mono text-sm">{secret.name}</span>
              <span className="text-xs text-muted">{secret.protected ? "protected" : "all branches"}</span>
              <span className="font-mono text-xs text-muted">••••••••</span>
              <Button size="sm" variant="ghost" icon="trash" onClick={() => remove(secret.name)} aria-label={`delete ${secret.name}`} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted">No secrets yet.</p>
      )}
      <form onSubmit={save} className="flex flex-col gap-2 sm:flex-row">
        <input value={name} onChange={(e) => setName(e.target.value.toUpperCase())} placeholder="NAME" className={cx(inputClass, "sm:w-48")} autoComplete="off" spellCheck={false} />
        <input value={value} onChange={(e) => setValue(e.target.value)} placeholder="value" type="password" className={inputClass} autoComplete="new-password" />
        <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={protectedSecret} onChange={(e) => setProtectedSecret(e.target.checked)} />Protected</label>
        <Button type="submit" icon="plus" disabled={!name.trim() || !value}>
          Save
        </Button>
      </form>
      {error && <p className="text-xs text-danger">{error}</p>}
      <p className="text-xs text-muted">
        Values are write-only, masked as <code className="font-mono">***</code> in logs and deleted from the microVM once loaded. A job only receives the secrets it lists in{" "}
        <code className="font-mono">secrets:</code>.
      </p>
    </div>
  );
}
