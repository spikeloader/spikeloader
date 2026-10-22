"use client";

import Link from "next/link";
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import type { Job, LabStatus } from "@/lib/types";
import { ConfirmProvider } from "./confirm";
import { StatusBadge, cx } from "./ui";

export async function api<T = unknown>(url: string, opts: { method?: string; body?: unknown } = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const res = await fetch(url, {
    method,
    cache: "no-store",
    headers: method === "GET" ? undefined : { "content-type": "application/json" },
    body: method === "GET" ? undefined : JSON.stringify(opts.body ?? {}),
  });
  if (res.status === 401) {
    // session expired or token rotated
    location.replace(`/login?next=${encodeURIComponent(location.pathname + location.search)}`);
    throw new Error("session expired");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error ?? res.statusText);
  return data as T;
}

/* ---------- lab state ---------- */

type LabCtx = { status: LabStatus | null; error: string | null; refresh: () => Promise<void> };
const Lab = createContext<LabCtx>({ status: null, error: null, refresh: async () => {} });
export const useLab = () => useContext(Lab);

function LabProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<LabStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    try {
      setStatus(await api<LabStatus>("/api/status"));
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      inflight.current = false;
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial polling load
    refresh();
    const t = setInterval(() => !document.hidden && refresh(), 3000);
    return () => clearInterval(t);
  }, [refresh]);

  return <Lab.Provider value={{ status, error, refresh }}>{children}</Lab.Provider>;
}

/* ---------- jobs ---------- */

type JobsCtx = {
  jobs: Job[];
  /** Fires an action that returns a job and tracks it in the dock. */
  submit: (url: string, opts?: { method?: string; body?: unknown }) => Promise<Job | null>;
  notify: (msg: string) => void;
};
const Jobs = createContext<JobsCtx>({ jobs: [], submit: async () => null, notify: () => {} });
export const useJobs = () => useContext(Jobs);

function JobsProvider({ children }: { children: ReactNode }) {
  const { refresh } = useLab();
  const [jobs, setJobs] = useState<Job[]>([]);
  const [docked, setDocked] = useState<string[]>([]);
  const [toast, setToast] = useState<string | null>(null);
  const running = useRef(new Set<string>());

  const load = useCallback(async () => {
    try {
      const list = await api<Job[]>("/api/jobs");
      const now = new Set(list.filter((j) => j.status === "running").map((j) => j.id));
      const finished = [...running.current].some((id) => !now.has(id));
      running.current = now;
      setJobs(list);
      if (finished) refresh();
    } catch {}
  }, [refresh]);

  const anyRunning = jobs.some((j) => j.status === "running");
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial polling load
    load();
    const t = setInterval(load, anyRunning ? 1000 : 5000);
    return () => clearInterval(t);
  }, [load, anyRunning]);

  const notify = useCallback((msg: string) => {
    setToast(msg);
    setTimeout(() => setToast((t) => (t === msg ? null : t)), 5000);
  }, []);

  const submit = useCallback<JobsCtx["submit"]>(
    async (url, opts = {}) => {
      try {
        const job = await api<Job>(url, { method: "POST", ...opts });
        running.current.add(job.id);
        setJobs((js) => [job, ...js]);
        setDocked((d) => [job.id, ...d].slice(0, 4));
        return job;
      } catch (e) {
        notify((e as Error).message);
        return null;
      }
    },
    [notify],
  );

  return (
    <Jobs.Provider value={{ jobs, submit, notify }}>
      {children}
      <JobDock jobs={docked.map((id) => jobs.find((j) => j.id === id)).filter((j): j is Job => !!j)} onClose={(id) => setDocked((d) => d.filter((x) => x !== id))} />
      {toast && (
        <div className="fixed bottom-4 left-1/2 z-50 -translate-x-1/2 rounded-lg border border-danger/40 bg-surface px-4 py-2 text-sm text-danger shadow-lg">
          {toast}
        </div>
      )}
    </Jobs.Provider>
  );
}

function JobDock({ jobs, onClose }: { jobs: Job[]; onClose: (id: string) => void }) {
  if (!jobs.length) return null;
  return (
    <div className="fixed right-4 bottom-4 z-40 flex w-[min(28rem,calc(100vw-2rem))] flex-col gap-2">
      {jobs.map((j) => (
        <JobCard key={j.id} job={j} onClose={() => onClose(j.id)} />
      ))}
    </div>
  );
}

export function JobOutput({ job, className }: { job: Job; className?: string }) {
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [job.output]);
  return (
    <pre ref={ref} className={cx("overflow-auto rounded-md bg-term p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-neutral-300", className)}>
      {job.output || (job.status === "running" ? "waiting for output…" : "(no output)")}
    </pre>
  );
}

function JobCard({ job, onClose }: { job: Job; onClose: () => void }) {
  const [open, setOpen] = useState(true);
  return (
    <div className="rounded-xl border border-border bg-surface shadow-xl">
      <div className="flex items-center gap-2 px-3 py-2">
        <StatusBadge state={job.status} label={job.status === "running" ? "running" : job.status === "ok" ? "done" : `error (${job.exitCode})`} tone={job.status === "running" ? "accent" : undefined} />
        <span className="min-w-0 flex-1 truncate text-sm">{job.title}</span>
        <button className="text-xs text-muted hover:text-fg" onClick={() => setOpen(!open)}>
          {open ? "hide" : "log"}
        </button>
        <button className="text-muted hover:text-fg" aria-label="close" onClick={onClose}>
          ×
        </button>
      </div>
      {open && (
        <div className="px-3 pb-3">
          <JobOutput job={job} className="max-h-48" />
          <Link href="/infra" className="mt-1.5 inline-block text-xs text-muted hover:text-fg">
            view events →
          </Link>
        </div>
      )}
    </div>
  );
}

export function Providers({ children }: { children: ReactNode }) {
  return (
    <LabProvider>
      <ConfirmProvider>
        <JobsProvider>{children}</JobsProvider>
      </ConfirmProvider>
    </LabProvider>
  );
}
