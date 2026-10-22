/** API contract with the Go backend: mirrors internal/ci/types.go (keep field names and nullability in sync). */

export type StepSpec = { name: string; run: string };

export type CacheSpec = { key: string; paths: string[]; files: string[] };

export type JobSpec = {
  /** Base name from the YAML (matrix jobs share it). */
  base: string;
  /** Display name: base, plus the matrix values. */
  name: string;
  stage: string;
  image: string;
  vcpus: number;
  mem: number;
  disk: string;
  timeoutS: number;
  /** Explicit dependencies (base names); null = every job from earlier stages. */
  needs: string[] | null;
  matrix: Record<string, string>;
  variables: Record<string, string>;
  secrets: string[];
  before: StepSpec[];
  steps: StepSpec[];
  after: StepSpec[];
  artifacts: { paths: string[]; when: "success" | "failure" | "always" } | null;
  cache: CacheSpec[];
  when: { branches: string[] | null; events: string[] | null; status: "success" | "failure" | "always" };
  manual: boolean;
  allowFailure: boolean;
  retry: number;
  depth: number;
};

export type Pipeline = {
  on: { push: string[] | null; schedule: string[]; manual: boolean; enabled: { push: boolean } };
  variables: Record<string, string>;
  stages: string[];
  jobs: JobSpec[];
};

export type CiEvent = "push" | "manual" | "schedule" | "webhook" | "retry";

export type JobStatus = "pending" | "queued" | "manual" | "running" | "success" | "failed" | "canceled" | "skipped";
export type RunStatus = "queued" | "running" | "waiting" | "success" | "failed" | "canceled";

export type StepState = {
  name: string;
  phase: "before" | "main" | "after";
  status: "pending" | "running" | "success" | "failed" | "skipped";
  startedAt: number | null;
  endedAt: number | null;
  exitCode: number | null;
};

export type CiJob = {
  idx: number;
  name: string;
  stage: string;
  image: string;
  /** Job indexes this job waits for. */
  deps: number[];
  status: JobStatus;
  /** Why it was skipped/failed without running (rules, upstream failure, config). */
  reason: string | null;
  manual: boolean;
  allowFailure: boolean;
  attempt: number;
  exitCode: number | null;
  vm: number | null;
  queuedAt: number | null;
  startedAt: number | null;
  endedAt: number | null;
  approvedBy: string | null;
  hasArtifacts: boolean;
  steps: StepState[];
  spec: JobSpec;
};

export type CiRun = {
  id: number;
  project: string;
  branch: string;
  sha: string;
  message: string;
  author: string;
  event: CiEvent;
  sourceRef: string;
  status: RunStatus;
  error: string | null;
  createdAt: number;
  startedAt: number | null;
  endedAt: number | null;
  canceled: boolean;
  /** Pipeline-level `variables:`. */
  variables: Record<string, string>;
  stages: string[];
  jobs: CiJob[];
};

export type Project = {
  id: string;
  name: string;
  url: string;
  defaultBranch: string;
  protectedBranches: string[];
  /** Seconds between polls of the remote; 0 = only webhooks/manual. */
  pollS: number;
  webhookToken: string;
  notifyUrl: string;
  notifyOn: "failure" | "always" | "never";
  createdAt: number;
  /** Last seen branch heads. */
  refs: Record<string, string>;
  lastSyncAt: number | null;
  lastError: string | null;
  /** Last minute (epoch/60000) each cron fired, keyed by expression. */
  scheduleFired: Record<string, number>;
};

/** Project as returned to the browser: no credentials in the URL, no token. */
export type ProjectView = Omit<Project, "webhookToken" | "url"> & { url: string; lastRun: RunSummary | null };

export type RunSummary = Omit<CiRun, "jobs" | "variables"> & {
  jobs: Pick<CiJob, "idx" | "name" | "stage" | "status">[];
};

export type CiImage = { name: string; size_bytes: number; used_bytes: number; built_at: number };
export type SecretInfo = { name: string; protected: boolean };

/** A built-in template, as listed by GET /api/ci (templates). */
export type TemplateInfo = { name: string; description: string; image: string; inputs: Record<string, string>; secrets: string[] };
