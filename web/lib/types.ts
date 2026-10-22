/** API contract with the Go backend (internal/lab, internal/jobs, internal/k8s): keep in sync with it. */

export type VmState = "running" | "paused" | "stopped";

export type MicroVM = {
  id: number;
  name: string;
  state: VmState;
  pid: number | null;
  ip: string;
  tap: string;
  tap_ip: string;
  mac: string;
  vcpus: number;
  mem_mib: number;
  rss_kb: number;
  cpu_pct: number;
  uptime_s: number;
  disk_used_bytes: number;
  disk_size_bytes: number;
  boot_args: string;
  /** "ci" for microVMs started by CI jobs; label = "run #N · job". */
  owner: string | null;
  label: string | null;
};

export type HostInfo = {
  hostname: string;
  kernel: string;
  firecracker: string;
  cpus: number;
  mem_total_kb: number;
  mem_avail_kb: number;
  disk_total_bytes: number;
  disk_avail_bytes: number;
  load: [number, number, number];
  uptime_s: number;
  guest_kernel: string;
  guest_rootfs: string;
  defaults: { vcpus: number; mem_mib: number };
};

export type LabStatus = {
  version: string;
  config: Record<string, string>;
  outer: { running: boolean; backend: string | null; reachable: boolean; provisioned: boolean };
  host: HostInfo | null;
  vms: MicroVM[];
  error: string | null;
};

export type JobStatus = "running" | "ok" | "error";

export type Job = {
  id: string;
  title: string;
  command: string;
  status: JobStatus;
  exitCode: number | null;
  output: string;
  startedAt: number;
  endedAt: number | null;
  /** Jobs of the same group never overlap ("outer" = outer VM lifecycle). */
  group?: string;
};

/* ---------- Kubernetes (read-only cluster view, internal/k8s in the Go backend) ---------- */

export type K8sWorkload = {
  kind: "Deployment" | "StatefulSet" | "DaemonSet";
  namespace: string;
  name: string;
  ready: number;
  desired: number;
  updated: number;
  images: string[];
  createdAt: number;
  health: "ok" | "progressing" | "degraded";
};

export type K8sOverview = {
  /** K8S_KUBECONFIG is set in .env. */
  configured: boolean;
  kubeconfig: string | null;
  error: string | null;
  cluster: { context: string; server: string; version: string; platform: string; metrics: boolean } | null;
  nodes: {
    name: string;
    ready: boolean;
    schedulable: boolean;
    roles: string[];
    version: string;
    os: string;
    runtime: string;
    cpuMilli: number;
    memBytes: number;
    cpuUsedMilli: number | null;
    memUsedBytes: number | null;
    pods: number;
    maxPods: number;
    createdAt: number;
  }[];
  namespaces: { name: string; phase: string; workloads: number; pods: number; podsRunning: number }[];
  workloads: K8sWorkload[];
  pods: { total: number; running: number; pending: number; failed: number; succeeded: number };
  problems: { namespace: string; name: string; phase: string; reason: string; restarts: number }[];
  warnings: { namespace: string; object: string; reason: string; message: string; count: number; at: number }[];
};
