# Roadmap

Ordered by value. Each item says why, how, where, and when it is done. Read
[backend.md](backend.md) first (packages, concurrency rules, API contract, security invariants).

Each numbered item has a ready-to-run Copilot prompt in `.github/prompts/` (`/fcagent`,
`/protected-secrets`, `/go-cli`, `/contract-tests`, `/log-limits`; `/verify` checks any change).

Status of the Go migration: **done** for the daemon (API, auth, CI engine, executor, git, PostgreSQL,
secrets, Kubernetes view, terminal, UI supervision), outer-VM agent and CI CLI (`cmd/spikeloader`).
`guest/fcctl` remains bash for debugging. Production CI lifecycle and execution use the Go `fcagent`.
The host scripts (`bootstrap.sh`, `host/*.sh`)
stay in bash on purpose: they are linear setup scripts.

---

## 1. `fcagent`: replace `guest/fcctl` with a Go agent in the outer VM

**Why.** Today every operation is an ssh + sudo + bash run of `fcctl`; job output is polled every
second with `tail -c` over ssh (`follow_vm`); and jobs are children of the daemon's ssh sessions, so
restarting the daemon fails running jobs.

**How.**
- New `cmd/fcagent`: a static binary installed by `guest/setup.sh` as a systemd service in the outer
  VM, running as root, listening on a Unix socket (`/run/fcagent.sock`).
- The daemon reaches it through the existing SSH connection (`ssh -W /run/fcagent.sock` or a
  forwarded socket via `-L`), so no new port is opened. Protocol: HTTP/JSON over the socket, plus
  a streaming endpoint for job output (chunked response or WebSocket).
- Port the subcommands with identical semantics first: `spawn`, `rm`, `ls`/`json`, `status`,
  `host-info`, `console`, `api`, `ssh`, `follow`, `image-build`, `images`, `image-rm`. Use
  `github.com/firecracker-microvm/firecracker-go-sdk` for the VMM, netlink for taps.
- Jobs become agent-owned: the agent starts the step runner in the microVM and keeps its output in a
  file; the daemon attaches/reattaches by job id and offset. A daemon restart then **reattaches**
  instead of failing the job (`ci.New` currently marks running jobs failed — change that to
  "reattach, or fail if the agent no longer knows the VM").
- Keep `guest/fcctl` working until the agent ships (drop-in: the agent can also expose a `fcctl`
  compatible CLI for debugging).

**Where.** `cmd/fcagent/`, `internal/agent/` (shared API types), `internal/sshx` (client),
`internal/ci/executor.go` (use the client instead of `FcctlStream`), `guest/setup.sh` (install).

**Phases.** All five phases are implemented: root Unix-socket service and provisioning; SSH client
and lab/reaper migration; SDK/netlink lifecycle, exec and image builds; agent-owned runner, streaming
and daemon reattachment; removal of executor fcctl paths. The owner authorized continuing phases
2–5 without intermediate review stops. See [backend.md](backend.md#outer-vm-agent) for the contract,
checkpoint/replay design and recovery boundary (after bundle preparation).

**Done when.** The end-to-end pipeline (see backend.md › Tests) passes; killing and restarting
`spikeloaderd` during a long step leaves the job running and its log complete; no per-second ssh
polling remains.

## 2. Protected branches for secrets

**Status: implemented.** Database migration, canonical run refs, API/TypeScript contract,
project/secret UI, executor policy and tests. Existing/new secrets default to protected;
unprotected branches, tags and detached commits are denied unless the secret is explicitly unprotected.

**Why.** Anyone able to push a branch can edit that branch's `.spikeloader.yml` to request any
project secret and send it elsewhere (masking does not stop exfiltration).

**How.** A per-project setting `protectedBranches: ["main", "release/*"]` (default: the default
branch) and a per-secret flag `protected: true` (default true for new secrets). The executor only
injects protected secrets when `run.Branch` matches a protected glob and the run event is not from an
unprotected ref; otherwise the job fails with "secret X is only available on protected branches".

**Where.** `Project` type (+ TS mirror), `secrets` table (add `protected boolean NOT NULL DEFAULT true`),
`Engine.SetSecret`, `execute` (secret selection), the project settings UI and `docs/ci.md`.

**Done when.** A push to an unprotected branch requesting a protected secret fails with that message;
the default branch still gets it; tests cover the matching.

## 3. CI CLI in Go

**Status: implemented.** The launcher builds/execs `cmd/spikeloader`; the Node client was removed.
Commands have HTTP fixture tests and a real disposable-pipeline script.

**Why.** Drop Node from the CLI (Node would remain only for the UI); one binary for operators.

**How.** `cmd/spikeloader` with the `ci` subcommands of `bin/spikeloader-ci.mjs` (same flags and
output), talking to the same API with the bearer token; `bin/spikeloader ci` execs it.

**Done when.** Every command in `spikeloader ci help` works identically; the `.mjs` file is removed.

## 4. Test coverage that guards the contract

**Status: implemented.** Golden JSON in `internal/api/testdata`, auth/guard/webhook tests,
fake-executor/run-store engine tests and `scripts/e2e.sh`. No VM or database is needed by `make test`.

- Golden-JSON tests for every API response type (`internal/api`), so a field rename or a `nil` slice
  turning into `null` fails CI.
- Engine tests with a fake executor/fcctl (inject an interface instead of calling `sshx` directly):
  DAG advance, `when.status`, manual approval, retries, cancel, retention, capacity/FIFO.
- `httptest` tests for auth, guard (Host/Origin/content type) and `public()` paths.
- An end-to-end script (`scripts/e2e.sh`) automating the demo pipeline against a running daemon.

## 5. Job log limits

**Status: implemented.** `CI_MAX_LOG_MB` defaults to 50 MiB. Rendered logs include one truncation
notice; guest/agent spools bound payload while retaining bounded step markers. Execution and
step-state parsing continue after the limit, including daemon recovery.

Cap each job log (e.g. 50 MB, `CI_MAX_LOG_MB` in `.env`): stop writing and mark "log truncated";
avoids a runaway job filling the disk.

## 6. Observability

Structured logs (`log/slog`) with run/job ids; `/api/metrics` in Prometheus format (queue length, jobs
by status, durations, outer VM state); optional OpenTelemetry later.

## 7. Multi-user (only when needed)

Users and roles (viewer / maintainer / admin) instead of the single token; per-project permissions;
audit log of actions; Firecracker **jailer** for every microVM (cgroups, seccomp, chroot) before any
shared or remote use. TLS termination stays in front (reverse proxy).

## Smaller items

- `ci/images/*.sh`: pin package versions or snapshot dates (Arch is rolling) when reproducibility
  matters.
- Webhook signature verification per provider (GitHub `X-Hub-Signature-256`, GitLab token) in
  addition to the project token.
- Cache eviction (size/age) for `.lab/ci/cache/`.
