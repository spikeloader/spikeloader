"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import type { ProjectView, RunSummary } from "@/lib/ci/types";
import { Icon } from "../icons";
import { api, useJobs } from "../providers";
import { Button, ButtonLink, Card, Empty, Field, Modal, Stat, cx, fmtDuration, inputClass } from "../ui";
import { CiSection, CiStatus, RunsList, ago, shortRepo, usePoll } from "./shared";

export type CiStats = {
  maxJobs: number;
  running: number;
  queued: number;
  outer: "off" | "starting" | "online";
  outerOk: boolean;
  capacity: { cpus: number; memMiB: number; usedVcpus: number; usedMemMiB: number } | null;
  idleShutdownMin: number;
  idleShutdownAt: number | null;
};

export type Overview = {
  projects: ProjectView[];
  runs: RunSummary[];
  stats: CiStats;
  globalSecrets: import("@/lib/ci/types").SecretInfo[];
  templates: { name: string; description: string }[];
  images: { name: string; description: string; from: string | null }[];
};

export function CiOverview() {
  const { data, error } = usePoll<Overview>("/api/ci", 3000);
  const [adding, setAdding] = useState(false);
  const s = data?.stats;
  const finished = data?.runs.filter((r) => r.status === "success" || r.status === "failed") ?? [];
  const rate = finished.length ? Math.round((finished.filter((r) => r.status === "success").length / finished.length) * 100) : null;
  const durations = finished.filter((r) => r.startedAt && r.endedAt).map((r) => (r.endedAt! - r.startedAt!) / 1000).sort((a, b) => a - b);
  const median = durations.length ? Math.round(durations[Math.floor(durations.length / 2)]) : null;

  return (
    <CiSection
      active="/ci"
      actions={
        <Button variant="primary" icon="plus" onClick={() => setAdding(true)}>
          New project
        </Button>
      }
    >
      {error && <p className="text-sm text-danger">{error}</p>}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat
          label="Running jobs"
          icon="activity"
          value={s?.running ?? "—"}
          hint={s?.capacity ? `${s.capacity.usedVcpus}/${s.capacity.cpus} vCPU · ${s.capacity.usedMemMiB}/${s.capacity.memMiB} MiB` : s?.outer === "off" ? "runners off — start on demand" : "runners starting…"}
        />
        <Stat label="Queued" icon="clock" value={s?.queued ?? "—"} hint={s?.queued ? (s.outerOk ? "waiting for capacity" : "starting the runners…") : "nothing waiting"} />
        <Stat label="Success rate" icon="check" value={rate === null ? "—" : `${rate}%`} tone={rate === null ? undefined : rate >= 80 ? "ok" : rate >= 50 ? "warn" : "danger"} hint={`last ${finished.length} finished runs`} />
        <Stat label="Median duration" icon="clock" value={median === null ? "—" : fmtDuration(median)} hint="of finished runs" />
      </div>

      {!data ? (
        <p className="text-sm text-muted">loading…</p>
      ) : !data.projects.length ? (
        <Empty
          icon="pipelines"
          title="No projects yet"
          action={
            <Button variant="primary" icon="plus" onClick={() => setAdding(true)}>
              Add your first project
            </Button>
          }
        >
          Add a git repository that has a <code className="font-mono">.spikeloader.yml</code>. New commits start runs automatically; the runners boot on demand.
        </Empty>
      ) : (
        <section className="flex flex-col gap-3">
          <h2 className="text-sm font-semibold">Projects</h2>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {data.projects.map((p) => (
              <ProjectCard key={p.id} p={p} runs={data.runs.filter((r) => r.project === p.id)} />
            ))}
          </div>
        </section>
      )}

      {data && data.runs.length > 0 && (
        <Card title="Recent runs" icon="list" flush actions={<ButtonLink href="/ci/runs" size="sm" variant="ghost">View all</ButtonLink>}>
          <RunsList runs={data.runs.slice(0, 8)} showProject />
        </Card>
      )}

      <Modal title="New project" open={adding} onClose={() => setAdding(false)}>
        <NewProject />
      </Modal>
    </CiSection>
  );
}

function ProjectCard({ p, runs }: { p: ProjectView; runs: RunSummary[] }) {
  const router = useRouter();
  const { notify } = useJobs();
  const [busy, setBusy] = useState(false);
  const last = p.lastRun;
  const history = [...runs].slice(0, 12).reverse();

  const run = async () => {
    setBusy(true);
    try {
      const r = await api<RunSummary>(`/api/ci/projects/${p.id}/run`, { method: "POST", body: {} });
      router.push(`/ci/runs/${r.id}`);
    } catch (e) {
      notify((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <div className="group flex min-w-0 flex-col rounded-xl border border-border bg-surface shadow-sm transition hover:border-muted/60">
      <Link href={`/ci/${p.id}`} className="flex flex-1 flex-col gap-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="truncate font-semibold group-hover:text-accent">{p.name}</div>
            <div className="truncate font-mono text-xs text-muted" title={p.url}>
              {shortRepo(p.url)}
            </div>
          </div>
          {last ? <CiStatus status={last.status} /> : <span className="text-xs text-muted">no runs</span>}
        </div>
        {last ? (
          <div className="min-w-0 text-xs text-muted">
            <div className="truncate text-sm text-fg">{last.message || "(no commit message)"}</div>
            <div className="mt-1 flex items-center gap-3">
              <span className="inline-flex min-w-0 items-center gap-1">
                <Icon name="branch" className="size-3" />
                <span className="truncate font-mono">{last.branch}</span>
              </span>
              <span>#{last.id}</span>
              <span>{ago(last.createdAt)}</span>
            </div>
          </div>
        ) : (
          <p className="text-xs text-muted">Waiting for the first commit, or start a run manually.</p>
        )}
        {p.lastError && (
          <p className="flex items-center gap-1.5 truncate text-xs text-danger">
            <Icon name="alert" className="size-3.5 shrink-0" />
            <span className="truncate">{p.lastError}</span>
          </p>
        )}
      </Link>
      <div className="flex items-center justify-between gap-3 border-t border-border px-4 py-2.5">
        <div className="flex items-end gap-0.5" title="last runs">
          {history.length ? (
            history.map((r) => (
              <Link
                key={r.id}
                href={`/ci/runs/${r.id}`}
                title={`#${r.id} ${r.status}`}
                className={cx("h-5 w-1.5 rounded-sm transition hover:opacity-70", { success: "bg-ok", failed: "bg-danger", running: "animate-pulse bg-accent", waiting: "bg-warn" }[r.status as string] ?? "bg-border")}
              />
            ))
          ) : (
            <span className="text-xs text-muted">—</span>
          )}
        </div>
        <Button size="sm" icon="play" onClick={run} disabled={busy}>
          {busy ? "Starting…" : `Run ${p.defaultBranch}`}
        </Button>
      </div>
    </div>
  );
}

function NewProject() {
  const router = useRouter();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [poll, setPoll] = useState(60);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const p = await api<ProjectView>("/api/ci/projects", { method: "POST", body: { name, url, defaultBranch: branch, pollS: poll } });
      router.push(`/ci/${p.id}`);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Field label="Name">
        <input value={name} onChange={(e) => setName(e.target.value)} className={inputClass} placeholder="my-app" autoFocus />
      </Field>
      <Field label="Git URL" hint="HTTPS (put a token in the URL for private repos), SSH (uses your keys) or a local file:// path.">
        <input value={url} onChange={(e) => setUrl(e.target.value)} className={inputClass} placeholder="git@github.com:me/app.git" spellCheck={false} />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Default branch">
          <input value={branch} onChange={(e) => setBranch(e.target.value)} className={inputClass} />
        </Field>
        <Field label="Check for commits every" hint="seconds · 0 = webhook only">
          <input type="number" min={0} value={poll} onChange={(e) => setPoll(Number(e.target.value))} className={inputClass} />
        </Field>
      </div>
      {error && <p className="text-xs text-danger">{error}</p>}
      <Button type="submit" variant="primary" disabled={busy || !name.trim() || !url.trim()}>
        {busy ? "Adding…" : "Add project"}
      </Button>
    </form>
  );
}
