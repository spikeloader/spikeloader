"use client";

import Link from "next/link";
import { useState } from "react";
import type { Job } from "@/lib/types";
import type { Overview } from "./ci/overview";
import { usePoll } from "./ci/shared";
import { Icon } from "./icons";
import { Kubernetes } from "./kubernetes";
import { JobOutput, useJobs, useLab } from "./providers";
import { Terminal } from "./terminal";
import { Button, Card, Empty, KV, Meter, Notice, PageHeader, SectionTitle, Stat, StatusBadge, cx, fmtBytes, fmtDuration } from "./ui";

const CONFIG: { title: string; keys: string[] }[] = [
  { title: "Outer VM", keys: ["OUTER_CPUS", "OUTER_MEM", "OUTER_DISK", "BACKEND", "SSH_PORT"] },
  { title: "CI/CD", keys: ["CI_MAX_JOBS", "CI_IDLE_SHUTDOWN_MIN", "CI_KEEP_RUNS"] },
  { title: "Firecracker", keys: ["FC_VERSION", "GUEST_KERNEL", "GUEST_ROOTFS"] },
];

const minutesUntil = (ts: number) => Math.max(Math.round((ts - Date.now()) / 60_000), 0);

/** "run #12 · build" → 12 */
const runOf = (label: string | null) => Number(label?.match(/run #(\d+)/)?.[1]) || null;

export function Infrastructure() {
  const { status } = useLab();
  const { data: ci } = usePoll<Overview>("/api/ci", 5000);
  const { jobs } = useJobs();
  const [shell, setShell] = useState(false);
  const h = status?.host;
  const o = status?.outer;
  const s = ci?.stats;
  const vms = (status?.vms ?? []).filter((v) => v.state !== "stopped");
  const state = o?.reachable ? "online" : o?.running || s?.outer === "starting" ? "starting" : "off";
  const idleIn = s?.idleShutdownAt ? minutesUntil(s.idleShutdownAt) : null;

  return (
    <>
      <PageHeader
        title="Infrastructure"
        description="The Kubernetes cluster your pipelines deploy to, and the runners that execute them. Runners are managed by the CI/CD engine: each job gets its own microVM, destroyed when the job ends."
      />

      <Kubernetes />

      <SectionTitle icon="server" title="Runners" badge={<StatusBadge state={state} label={{ online: "online", starting: "starting", off: "off" }[state]} />} meta="Outer VM and job microVMs — nothing to start or size by hand." />

      <Notice>
        {state === "off" ? (
          <>
            The outer VM is <span className="font-medium text-fg">off</span>. It boots by itself as soon as a job is queued (about 20 s, or 2-3 min the very first time).
          </>
        ) : state === "starting" ? (
          <>The outer VM is booting because a job is waiting. Queued jobs start as soon as it answers.</>
        ) : s?.running || s?.queued ? (
          <>
            Running {s.running} job{s.running === 1 ? "" : "s"}
            {s.queued ? `, ${s.queued} waiting for capacity` : ""}. Jobs are started oldest first while their vCPUs and memory fit in the outer VM
            {s.maxJobs ? ` (capped at ${s.maxJobs} by CI_MAX_JOBS)` : ""}.
          </>
        ) : (
          <>
            Idle.{" "}
            {idleIn !== null ? (
              <>
                The outer VM stops in <span className="font-medium text-fg">{idleIn} min</span> if no job arrives
              </>
            ) : (
              <>Idle shutdown is disabled (CI_IDLE_SHUTDOWN_MIN=0)</>
            )}
            ; the next job starts it again.
          </>
        )}
      </Notice>

      {/* same grid lines as the columns below: three stats over the main column, one over the sidebar */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-[repeat(3,minmax(0,1fr))_22rem] lg:gap-6">
        <Stat label="Job microVMs" icon="box" value={vms.length} hint={s ? `${s.queued} queued` : "—"} />
        <Stat label="vCPUs in use" icon="cpu" value={s?.capacity ? `${s.capacity.usedVcpus}/${s.capacity.cpus}` : "—"} hint={h ? `${h.cpus} vCPUs, overcommit 2×` : "outer VM off"} />
        <Stat label="Memory reserved" icon="memory" value={s?.capacity ? fmtBytes(s.capacity.usedMemMiB * 1048576) : "—"} hint={s?.capacity ? `of ${fmtBytes(s.capacity.memMiB * 1048576)} for jobs` : "outer VM off"} />
        <Stat label="Disk used" icon="disk" value={h ? fmtBytes(h.disk_total_bytes - h.disk_avail_bytes) : "—"} hint={h ? `of ${fmtBytes(h.disk_total_bytes)} (images, workspaces)` : "outer VM off"} />
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex min-w-0 flex-col gap-6">
          <Card title="Job microVMs" icon="box" description="One per running job; click to inspect or open a shell for debugging." flush>
            {!vms.length ? (
              <div className="p-4">
                <Empty icon="box" title="No jobs running">
                  MicroVMs appear here while CI jobs run.
                </Empty>
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {vms.map((vm) => {
                  const run = runOf(vm.label);
                  return (
                    <li key={vm.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <Link href={`/vms/${vm.id}`} className="font-mono text-sm font-semibold hover:text-accent">
                            {vm.name}
                          </Link>
                          <StatusBadge state={vm.state} />
                        </div>
                        <div className="mt-0.5 truncate text-xs text-muted">
                          {run ? (
                            <Link href={`/ci/runs/${run}`} className="hover:text-fg">
                              {vm.label}
                            </Link>
                          ) : (
                            (vm.label ?? "not started by CI")
                          )}
                        </div>
                      </div>
                      <div className="grid grid-cols-3 gap-4 font-mono text-xs text-muted tabular-nums">
                        <span title="vCPU">
                          {vm.vcpus} vCPU · {vm.cpu_pct.toFixed(0)}%
                        </span>
                        <span title="memory in use / reserved">
                          {fmtBytes(vm.rss_kb * 1024)} / {vm.mem_mib} MiB
                        </span>
                        <span title="uptime">{fmtDuration(vm.uptime_s)}</span>
                      </div>
                      <Link href={`/vms/${vm.id}`} className="inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
                        Inspect <Icon name="chevron" className="size-3.5" />
                      </Link>
                    </li>
                  );
                })}
              </ul>
            )}
          </Card>

          <Events jobs={jobs} />

          <Card
            title="Outer VM shell"
            icon="terminal"
            description="For debugging only — the engine provisions and cleans the outer VM itself."
            actions={
              <Button size="sm" icon={shell ? "x" : "terminal"} onClick={() => setShell(!shell)} disabled={!o?.reachable}>
                {shell ? "Close" : "Open shell"}
              </Button>
            }
          >
            {shell && o?.reachable ? (
              <Terminal target="outer" height={420} />
            ) : (
              <p className="text-sm text-muted">{o?.reachable ? "Opens a shell as the lab user (sudo allowed)." : "Available while the outer VM is online."}</p>
            )}
          </Card>
        </div>

        <div className="flex flex-col gap-6">
          <Card title="Outer VM" icon="server" description="The VM that hosts the job microVMs." actions={<StatusBadge state={state} />}>
            {h ? (
              <div className="flex flex-col gap-4">
                <KV
                  rows={[
                    ["Host", h.hostname],
                    ["Backend", o?.backend ?? "—"],
                    ["Kernel", h.kernel],
                    ["Firecracker", h.firecracker.replace("Firecracker ", "")],
                    ["Uptime", fmtDuration(h.uptime_s)],
                  ]}
                />
                <Meter label="Memory" icon="memory" value={h.mem_total_kb - h.mem_avail_kb} max={h.mem_total_kb} detail={`${fmtBytes((h.mem_total_kb - h.mem_avail_kb) * 1024)} / ${fmtBytes(h.mem_total_kb * 1024)}`} />
                <Meter label="Disk" icon="disk" value={h.disk_total_bytes - h.disk_avail_bytes} max={h.disk_total_bytes} detail={`${fmtBytes(h.disk_total_bytes - h.disk_avail_bytes)} / ${fmtBytes(h.disk_total_bytes)}`} />
                <Meter label={`Load (${h.cpus} vCPUs)`} icon="cpu" value={h.load[0]} max={h.cpus} detail={h.load.map((l) => l.toFixed(2)).join("  ")} />
              </div>
            ) : (
              <p className="text-sm text-muted">{state === "starting" ? "Booting…" : "Off — starts when a job is queued."}</p>
            )}
            {status?.error && <p className="mt-3 font-mono text-xs text-danger">{status.error}</p>}
          </Card>

          <Card title="Configuration" icon="sliders" description=".env in the repository; applied the next time the outer VM starts.">
            <div className="flex flex-col gap-4">
              {CONFIG.map((g) => (
                <div key={g.title}>
                  <div className="mb-1.5 text-xs font-medium text-muted">{g.title}</div>
                  <KV rows={g.keys.map((k) => [k, status?.config[k] || (k === "CI_MAX_JOBS" ? "0 (auto)" : "—")])} />
                </div>
              ))}
            </div>
          </Card>

        </div>
      </div>
    </>
  );
}

function Events({ jobs: all }: { jobs: Job[] }) {
  const [open, setOpen] = useState<string | null>(null);
  const jobs = all.filter((j) => j.group === "outer");
  return (
    <Card title="Events" icon="activity" description="Outer VM starts, idle shutdowns and maintenance (kept across panel restarts)." flush>
      {!jobs.length ? (
        <div className="p-4">
          <Empty icon="activity" title="No events yet" />
        </div>
      ) : (
        <ul className="divide-y divide-border">
          {jobs.slice(0, 15).map((j) => (
            <li key={j.id}>
              <button onClick={() => setOpen(open === j.id ? null : j.id)} className="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-surface-2/50">
                <StatusBadge state={j.status} label={j.status === "running" ? "running" : j.status === "ok" ? "done" : `error (${j.exitCode})`} tone={j.status === "running" ? "accent" : undefined} />
                <span className="min-w-0 flex-1 truncate text-sm">{j.title}</span>
                <span className="shrink-0 text-xs text-muted">{new Date(j.startedAt).toLocaleString("en-US", { dateStyle: "short", timeStyle: "short" })}</span>
                <Icon name="chevron" className={cx("size-4 shrink-0 text-muted transition", open === j.id && "rotate-90")} />
              </button>
              {open === j.id && (
                <div className="px-4 pb-3">
                  <div className="mb-1.5 font-mono text-xs text-muted">$ {j.command}</div>
                  <JobOutput job={j} className="max-h-80" />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
