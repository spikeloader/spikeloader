"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Fragment, use, useEffect, useRef, useState, type ReactNode } from "react";
import type { CiJob, CiRun, RunSummary } from "@/lib/ci/types";
import { Icon } from "../icons";
import { api, useJobs } from "../providers";
import { Button, Card, Empty, KV, MetaItem, Notice, PageHeader, cx } from "../ui";
import { CiStatus, EVENT_ICON, StatusIcon, ago, duration, usePoll } from "./shared";

const ACTIVE = ["queued", "running", "waiting"];

export function CiRunView({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const { data: run, error, reload } = usePoll<CiRun>(`/api/ci/runs/${id}`, 2000);
  const { notify } = useJobs();
  const router = useRouter();
  const path = usePathname();
  const search = useSearchParams();

  if (error && !run) return <Empty icon="alert" title={error} action={<Link href="/ci" className="text-sm text-accent">Back to pipelines</Link>} />;
  if (!run) return <div className="text-sm text-muted">loading…</div>;

  const picked = search.has("job") ? Number(search.get("job")) : -1;
  const job =
    run.jobs[picked] ??
    run.jobs.find((j) => j.status === "running") ??
    run.jobs.find((j) => j.status === "failed") ??
    run.jobs.find((j) => j.status === "manual") ??
    run.jobs[0];
  const select = (idx: number) => router.replace(`${path}?job=${idx}`, { scroll: false });

  const act = async (action: string, idx?: number) => {
    try {
      const r = await api<RunSummary>(`/api/ci/runs/${run.id}`, { method: "POST", body: { action, job: idx } });
      if (action === "rerun") router.push(`/ci/runs/${r.id}`);
      else reload();
    } catch (e) {
      notify((e as Error).message);
    }
  };

  const counts = run.jobs.reduce<Record<string, number>>((m, j) => ((m[j.status] = (m[j.status] ?? 0) + 1), m), {});

  return (
    <>
      <PageHeader
        back={{ href: `/ci/${run.project}`, label: run.project }}
        title={`Run #${run.id}`}
        badge={<CiStatus status={run.status} />}
        description={<span className="text-base text-fg">{run.message || "(no commit message)"}</span>}
        meta={
          <>
            <MetaItem icon="branch">
              <span className="font-mono">{run.branch}</span>
            </MetaItem>
            <MetaItem icon="commit" title={run.sha}>
              <span className="font-mono">{run.sha.slice(0, 10)}</span>
            </MetaItem>
            {run.author && <MetaItem icon="user">{run.author}</MetaItem>}
            <MetaItem icon={EVENT_ICON[run.event]}>
              {run.event} · {ago(run.createdAt)}
            </MetaItem>
            <MetaItem icon="clock">{duration(run.startedAt, run.endedAt)}</MetaItem>
          </>
        }
        actions={
          <>
            {ACTIVE.includes(run.status) && (
              <Button variant="danger" icon="stop" onClick={() => act("cancel")}>
                Cancel
              </Button>
            )}
            <Button icon="refresh" onClick={() => act("rerun")}>
              Re-run
            </Button>
          </>
        }
      />

      {run.error && (
        <Notice tone="danger">
          <div className="font-medium">The pipeline configuration is invalid</div>
          <pre className="mt-1 font-mono text-xs whitespace-pre-wrap">{run.error}</pre>
        </Notice>
      )}
      {run.status === "waiting" && (
        <Notice tone="warn">
          A job is waiting for approval. Review it and press <span className="font-medium">Approve</span> to continue.
        </Notice>
      )}

      {run.jobs.length > 0 && (
        <Card
          title="Pipeline"
          icon="pipelines"
          actions={
            <span className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
              {Object.entries(counts).map(([s, n]) => (
                <span key={s} className="inline-flex items-center gap-1">
                  <StatusIcon status={s} className="size-3.5" />
                  {n} {s}
                </span>
              ))}
            </span>
          }
        >
          <div className="-mx-4 overflow-x-auto px-4 pb-1">
            <div className="flex min-w-max items-stretch">
              {run.stages.map((stage, si) => (
                <Fragment key={stage}>
                  {si > 0 && (
                    <div className="flex w-8 items-center justify-center text-border">
                      <Icon name="chevron" className="size-5" />
                    </div>
                  )}
                  <div className="flex w-56 flex-col gap-2">
                    <div className="flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
                      <span className="grid size-5 place-items-center rounded-full bg-surface-2 text-[10px]">{si + 1}</span>
                      {stage}
                    </div>
                    {run.jobs
                      .filter((j) => j.stage === stage)
                      .map((j) => (
                        <JobCard key={j.idx} job={j} selected={j.idx === job?.idx} onSelect={() => select(j.idx)} onAct={(a) => act(a, j.idx)} />
                      ))}
                  </div>
                </Fragment>
              ))}
            </div>
          </div>
        </Card>
      )}

      {job && <JobPanel run={run} job={job} onAct={(a) => act(a, job.idx)} />}
    </>
  );
}

const BAR: Record<string, string> = { success: "bg-ok", failed: "bg-danger", running: "bg-accent", manual: "bg-warn" };

function JobCard({ job, selected, onSelect, onAct }: { job: CiJob; selected: boolean; onSelect: () => void; onAct: (a: string) => void }) {
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onSelect}
      onKeyDown={(e) => e.key === "Enter" && onSelect()}
      className={cx(
        "relative cursor-pointer overflow-hidden rounded-lg border bg-bg py-2.5 pr-3 pl-4 transition",
        selected ? "border-accent ring-2 ring-accent/20" : "border-border hover:border-muted",
      )}
    >
      <span className={cx("absolute inset-y-0 left-0 w-1", BAR[job.status] ?? "bg-border", job.status === "running" && "animate-pulse")} />
      <div className="flex items-center gap-2">
        <StatusIcon status={job.status} className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-sm font-medium">{job.name}</span>
        <span className="font-mono text-[11px] text-muted">{job.startedAt ? duration(job.startedAt, job.endedAt) : ""}</span>
      </div>
      <div className="mt-1 flex items-center justify-between gap-2 pl-6 text-[11px] text-muted">
        <span className="truncate">{job.reason ?? (job.status === "manual" ? "awaiting approval" : job.image)}</span>
        {job.status === "manual" && (
          <Button
            size="sm"
            variant="primary"
            onClick={(e) => {
              e.stopPropagation();
              onAct("approve");
            }}
          >
            Approve
          </Button>
        )}
      </div>
    </div>
  );
}

function JobPanel({ run, job, onAct }: { run: CiRun; job: CiJob; onAct: (a: string) => void }) {
  const s = job.spec;
  const canRetry = ["failed", "canceled", "success"].includes(job.status) || (job.status === "skipped" && job.reason !== "rules");
  const steps = job.steps.length ? job.steps : [...s.before, ...s.steps, ...s.after].map((st) => ({ name: st.name, status: "pending", startedAt: null, endedAt: null }));

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <StatusIcon status={job.status} />
          {job.name}
        </span>
      }
      description={`${job.stage} · ${s.image} · ${s.vcpus} vCPU · ${s.mem} MiB · ${s.disk}`}
      flush
      actions={
        <>
          <CiStatus status={job.status} />
          {job.status === "manual" && (
            <Button size="sm" variant="primary" icon="check" onClick={() => onAct("approve")}>
              Approve
            </Button>
          )}
          {canRetry && (
            <Button size="sm" icon="refresh" onClick={() => onAct("retry")} title="Re-run this job and everything after it">
              Retry
            </Button>
          )}
          {job.hasArtifacts && (
            <a href={`/api/ci/runs/${run.id}/jobs/${job.idx}/artifacts`} className="inline-flex h-7 items-center gap-1.5 rounded-lg border border-border bg-surface px-2.5 text-xs font-medium hover:bg-surface-2">
              <Icon name="download" className="size-3.5" />
              Artifacts
            </a>
          )}
        </>
      }
    >
      <div className="grid lg:grid-cols-[16rem_minmax(0,1fr)]">
        <aside className="flex flex-col gap-4 border-b border-border p-4 lg:border-r lg:border-b-0">
          <div>
            <div className="mb-2 text-xs font-medium text-muted">Steps</div>
            <ol className="flex flex-col gap-1">
              {steps.map((st, i) => (
                <li key={i} className="flex items-center gap-2 rounded-md px-1.5 py-1 text-xs hover:bg-surface-2/60">
                  <StatusIcon status={st.status} className="size-3.5 shrink-0" />
                  <span className="min-w-0 flex-1 truncate font-mono" title={st.name}>
                    {st.name}
                  </span>
                  <span className="font-mono text-muted">{st.startedAt ? duration(st.startedAt, st.endedAt) : ""}</span>
                </li>
              ))}
            </ol>
          </div>
          <KV
            rows={[
              ["microVM", job.vm ? <Link key="vm" href={`/vms/${job.vm}`} className="text-accent">vm{job.vm}</Link> : "—"],
              ["Attempt", `${job.attempt}${s.retry ? ` of ${s.retry + 1}` : ""}`],
              ["Exit code", job.exitCode ?? "—"],
              ["Started", job.startedAt ? ago(job.startedAt) : "—"],
              ["Duration", duration(job.startedAt, job.endedAt)],
              ...(job.approvedBy ? ([["Approved by", job.approvedBy]] as [string, ReactNode][]) : []),
            ]}
          />
        </aside>
        <LogView key={`${run.id}-${job.idx}`} runId={run.id} idx={job.idx} status={job.status} reason={job.reason} />
      </div>
    </Card>
  );
}

/** Streams the job log by polling with a byte offset. */
function LogView({ runId, idx, status, reason }: { runId: number; idx: number; status: string; reason: string | null }) {
  const [text, setText] = useState("");
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(true);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let offset = 0;
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const load = async () => {
      try {
        const r = await api<{ text: string; offset: number; reset: boolean; done: boolean }>(`/api/ci/runs/${runId}/jobs/${idx}/log?offset=${offset}`);
        if (stop) return;
        offset = r.offset;
        if (r.reset) setText(r.text);
        else if (r.text) setText((t) => t + r.text);
        timer = setTimeout(load, r.text.length >= 512 * 1024 ? 0 : r.done ? 5000 : 1000);
      } catch {
        if (!stop) timer = setTimeout(load, 3000);
      }
    };
    load();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [runId, idx]);

  useEffect(() => {
    if (follow && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [text, follow]);

  const lines = text ? text.replace(/\n$/, "").split("\n") : [];
  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-border px-4 py-2 text-xs text-muted">
        <span className="inline-flex items-center gap-1.5">
          <Icon name="terminal" className="size-3.5" />
          Log
        </span>
        <label className="inline-flex items-center gap-1.5">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} className="accent-[var(--accent)]" />
          follow
        </label>
        <label className="inline-flex items-center gap-1.5">
          <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} className="accent-[var(--accent)]" />
          wrap lines
        </label>
        <a href={`/api/ci/runs/${runId}/jobs/${idx}/log?raw=1`} className="ml-auto inline-flex items-center gap-1 hover:text-fg">
          <Icon name="download" className="size-3.5" />
          raw log
        </a>
      </div>
      <div ref={ref} className="h-[620px] overflow-auto bg-term py-2 font-mono text-[12px] leading-[1.6] text-neutral-300 lg:rounded-br-xl">
        {lines.length ? (
          <table className="w-full border-collapse">
            <tbody>
              {lines.map((line, i) => (
                <tr key={i} className="hover:bg-white/[0.03]">
                  <td className="w-12 pr-3 text-right align-top text-neutral-600 select-none">{i + 1}</td>
                  <td className={cx("pr-4", wrap ? "break-all whitespace-pre-wrap" : "whitespace-pre")}>
                    <LogLine line={line} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div className="px-4 text-neutral-500">
            {{
              manual: "Waiting for approval — the job starts when someone presses Approve.",
              pending: "Waiting for the jobs it depends on.",
              queued: "Queued — starts when the outer VM has capacity (it boots by itself if it is off).",
              skipped: `Skipped${reason ? `: ${reason}` : ""}.`,
              canceled: "Canceled before it started.",
            }[status] ?? "waiting for output…"}
          </div>
        )}
      </div>
    </div>
  );
}

const ANSI_FG: Record<number, string> = {
  30: "text-neutral-500", 31: "text-red-400", 32: "text-green-400", 33: "text-yellow-300", 34: "text-blue-400", 35: "text-fuchsia-400", 36: "text-cyan-300", 37: "text-neutral-200",
  90: "text-neutral-500", 91: "text-red-300", 92: "text-green-300", 93: "text-yellow-200", 94: "text-blue-300", 95: "text-fuchsia-300", 96: "text-cyan-200", 97: "text-white",
};

/** Renders SGR colors/bold from tool output (everything else was stripped by the engine). */
function ansi(line: string) {
  const parts = line.split(/\x1b\[([0-9;]*)m/);
  if (parts.length === 1) return line;
  const out: ReactNode[] = [];
  let fg = "";
  let bold = false;
  parts.forEach((part, i) => {
    if (i % 2 === 0) {
      if (part) out.push(<span key={i} className={cx(fg, bold && "font-semibold")}>{part}</span>);
      return;
    }
    for (const code of (part || "0").split(";").map(Number)) {
      if (code === 0) [fg, bold] = ["", false];
      else if (code === 1) bold = true;
      else if (code === 22) bold = false;
      else if (code === 39) fg = "";
      else if (ANSI_FG[code]) fg = ANSI_FG[code];
    }
  });
  return out;
}

function LogLine({ line }: { line: string }) {
  if (line.startsWith("▶")) return <span className="font-semibold text-accent">{line}</span>;
  if (line.startsWith("✓")) return <span className="text-green-400">{line}</span>;
  if (line.startsWith("✗")) return <span className="font-semibold text-red-400">{line}</span>;
  if (line.startsWith("○") || line.startsWith("↻")) return <span className="text-neutral-500">{line}</span>;
  if (line.startsWith("  │")) return <span className="text-neutral-500">{ansi(line)}</span>;
  return <>{line ? ansi(line) : " "}</>;
}
