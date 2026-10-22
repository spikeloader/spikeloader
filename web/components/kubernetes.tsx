"use client";

import { useState, type ReactNode } from "react";
import type { K8sOverview, K8sWorkload } from "@/lib/types";
import { usePoll } from "./ci/shared";
import { Button, Card, Empty, KV, MetaItem, Meter, Notice, SectionTitle, Stat, StatusBadge, cx, fmtBytes } from "./ui";

/** kube-system, kube-public, kube-node-lease: hidden from the workload list unless asked for. */
const isSystem = (ns: string) => ns.startsWith("kube-");

/** "3d", "5h", "12m", "40s" */
function age(ts: number) {
  const s = Math.max(Math.floor((Date.now() - ts) / 1000), 0);
  if (s >= 86400) return `${Math.floor(s / 86400)}d`;
  if (s >= 3600) return `${Math.floor(s / 3600)}h`;
  if (s >= 60) return `${Math.floor(s / 60)}m`;
  return `${s}s`;
}

const cores = (milli: number) => (milli >= 1000 ? `${(milli / 1000).toFixed(milli % 1000 ? 1 : 0)}` : `${Math.round(milli)}m`);

const HEALTH: Record<K8sWorkload["health"], { label: string; tone: "ok" | "accent" | "danger" }> = {
  ok: { label: "ready", tone: "ok" },
  progressing: { label: "rolling out", tone: "accent" },
  degraded: { label: "degraded", tone: "danger" },
};

/** Read-only view of the cluster the pipelines deploy to (K8S_KUBECONFIG in .env). */
export function Kubernetes() {
  const { data: k, error } = usePoll<K8sOverview>("/api/k8s", 10_000);
  const [system, setSystem] = useState(false);

  const heading = (badge?: ReactNode, meta?: ReactNode) => <SectionTitle icon="kube" title="Kubernetes" badge={badge} meta={meta} />;

  if (!k) {
    return (
      <section className="flex flex-col gap-4">
        {heading()}
        {error ? <Notice tone="danger">{error}</Notice> : <p className="text-sm text-muted">Loading…</p>}
      </section>
    );
  }

  if (!k.configured) {
    return (
      <section className="flex flex-col gap-4">
        {heading()}
        <Empty icon="kube" title="No cluster configured">
          Set <code className="font-mono text-xs">K8S_KUBECONFIG</code> in .env to a kubeconfig (token or client certificate) to see the cluster your pipelines deploy to.
        </Empty>
      </section>
    );
  }

  const c = k.cluster;
  const meta = c && (
    <>
      <MetaItem icon="link">{c.server}</MetaItem>
      <MetaItem icon="key">context {c.context}</MetaItem>
      {c.version && <MetaItem icon="info">{c.version}</MetaItem>}
      {k.kubeconfig && <MetaItem icon="file">{k.kubeconfig}</MetaItem>}
    </>
  );

  if (k.error) {
    return (
      <section className="flex flex-col gap-4">
        {heading(<StatusBadge state="error" label="unreachable" />, meta)}
        <Notice tone="danger">{k.error}</Notice>
      </section>
    );
  }

  const nodesReady = k.nodes.filter((n) => n.ready).length;
  const healthy = k.workloads.filter((w) => w.health === "ok").length;
  const shown = k.workloads.filter((w) => system || !isSystem(w.namespace));
  const hiddenCount = k.workloads.length - shown.length;
  const namespaces = k.namespaces.filter((n) => system || !isSystem(n.name));

  return (
    <section className="flex flex-col gap-6">
      {heading(<StatusBadge state={nodesReady === k.nodes.length ? "online" : "error"} label={nodesReady === k.nodes.length ? "connected" : "nodes not ready"} />, meta)}

      {/* same grid lines as the runner section below */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-[repeat(3,minmax(0,1fr))_22rem] lg:gap-6">
        <Stat label="Workloads ready" icon="pipelines" value={`${healthy}/${k.workloads.length}`} hint={`${k.workloads.length - healthy} rolling out or degraded`} tone={healthy === k.workloads.length ? undefined : "warn"} />
        <Stat label="Pods running" icon="box" value={`${k.pods.running}/${k.pods.total - k.pods.succeeded}`} hint={`${k.pods.pending} pending · ${k.pods.failed} failed`} />
        <Stat label="Unhealthy pods" icon="alert" value={k.problems.length} hint={k.problems.length ? "see below" : "all pods healthy"} tone={k.problems.length ? "danger" : undefined} />
        <Stat label="Nodes ready" icon="server" value={`${nodesReady}/${k.nodes.length}`} hint={`${k.namespaces.length} namespaces`} tone={nodesReady === k.nodes.length ? undefined : "danger"} />
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex min-w-0 flex-col gap-6">
          <Card
            title="Workloads"
            icon="pipelines"
            description="Deployments, StatefulSets and DaemonSets across namespaces."
            actions={
              <Button size="sm" variant={system ? "secondary" : "ghost"} onClick={() => setSystem(!system)}>
                {system ? "Hide system" : `Show system${hiddenCount ? ` (${hiddenCount})` : ""}`}
              </Button>
            }
            flush
          >
            {!shown.length ? (
              <div className="p-4">
                <Empty icon="pipelines" title="No workloads yet">
                  Deploys from your pipelines show up here.
                </Empty>
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {shown.map((w) => (
                  <li key={`${w.kind}/${w.namespace}/${w.name}`} className="flex items-center gap-3 px-4 py-2.5">
                    <StatusBadge state={w.health} label={HEALTH[w.health].label} tone={HEALTH[w.health].tone} pulse={w.health === "progressing"} />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm">
                        <span className="text-muted">{w.namespace}/</span>
                        <span className="font-medium">{w.name}</span>
                      </div>
                      <div className="truncate font-mono text-xs text-muted" title={w.images.join("\n")}>
                        {w.kind} · {w.images.join(", ")}
                      </div>
                    </div>
                    <span className="shrink-0 font-mono text-xs tabular-nums" title="ready / desired">
                      {w.ready}/{w.desired}
                    </span>
                    <span className="w-8 shrink-0 text-right text-xs text-muted tabular-nums" title={new Date(w.createdAt).toLocaleString()}>
                      {age(w.createdAt)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          {k.problems.length > 0 && (
            <Card title="Unhealthy pods" icon="alert" description="Pods that are not ready, waiting or crashing." flush>
              <ul className="divide-y divide-border">
                {k.problems.map((p) => (
                  <li key={`${p.namespace}/${p.name}`} className="flex items-center gap-3 px-4 py-2.5">
                    <StatusBadge state="error" label={p.reason} />
                    <span className="min-w-0 flex-1 truncate text-sm">
                      <span className="text-muted">{p.namespace}/</span>
                      {p.name}
                    </span>
                    <span className="shrink-0 font-mono text-xs text-muted tabular-nums">{p.restarts} restarts</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          <Card title="Warning events" icon="activity" description="Most recent Warning events reported by the cluster." flush>
            {!k.warnings.length ? (
              <div className="p-4">
                <Empty icon="check" title="No warnings" />
              </div>
            ) : (
              <ul className="divide-y divide-border">
                {k.warnings.map((e, i) => (
                  <li key={i} className="flex flex-col gap-0.5 px-4 py-2.5">
                    <div className="flex items-center gap-3">
                      <span className="shrink-0 text-xs font-semibold text-warn">{e.reason}</span>
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted">
                        {e.namespace && `${e.namespace}/`}
                        {e.object}
                      </span>
                      <span className="shrink-0 text-xs text-muted tabular-nums">
                        {e.count > 1 && `${e.count}× · `}
                        {age(e.at)} ago
                      </span>
                    </div>
                    <p className="line-clamp-2 text-xs">{e.message}</p>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>

        <div className="flex flex-col gap-6">
          {k.nodes.map((n) => (
            <Card key={n.name} title={n.name} icon="server" description={n.roles.length ? n.roles.join(", ") : "worker"} actions={<StatusBadge state={n.ready ? "online" : "error"} label={n.ready ? (n.schedulable ? "ready" : "cordoned") : "not ready"} tone={n.ready && !n.schedulable ? "warn" : undefined} />}>
              <div className="flex flex-col gap-4">
                {n.cpuUsedMilli !== null && <Meter label={`CPU (${cores(n.cpuMilli)} cores)`} icon="cpu" value={n.cpuUsedMilli} max={n.cpuMilli} detail={`${cores(n.cpuUsedMilli)} / ${cores(n.cpuMilli)}`} />}
                {n.memUsedBytes !== null && <Meter label="Memory" icon="memory" value={n.memUsedBytes} max={n.memBytes} detail={`${fmtBytes(n.memUsedBytes)} / ${fmtBytes(n.memBytes)}`} />}
                <Meter label="Pods" icon="box" value={n.pods} max={n.maxPods} detail={`${n.pods} / ${n.maxPods}`} />
                {n.cpuUsedMilli === null && <p className="text-xs text-muted">CPU and memory usage need metrics-server in the cluster.</p>}
                <KV
                  rows={[
                    ["Kubelet", n.version],
                    ["Runtime", n.runtime],
                    ["OS", n.os],
                    ["Age", age(n.createdAt)],
                  ]}
                />
              </div>
            </Card>
          ))}

          <Card title="Namespaces" icon="list" description="Running pods / pods per namespace." flush>
            <ul className="divide-y divide-border">
              {namespaces.map((n) => (
                <li key={n.name} className="flex items-center gap-3 px-4 py-2">
                  <span className={cx("min-w-0 flex-1 truncate font-mono text-xs", isSystem(n.name) && "text-muted")}>{n.name}</span>
                  {n.phase !== "Active" && <StatusBadge state="pending" label={n.phase.toLowerCase()} />}
                  <span className="shrink-0 text-xs text-muted tabular-nums">
                    {n.workloads} workloads · {n.podsRunning}/{n.pods} pods
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      </div>
    </section>
  );
}
