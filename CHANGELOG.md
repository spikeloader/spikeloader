# Changelog

## Unreleased

### Public source distribution
- Add Apache-2.0 licensing, contribution and security guidance, a sanitized `.env.example`,
  Git exclusions for local state/credentials, and clone/setup instructions for the public repository.
- Initialize missing local configuration during bootstrap without overwriting existing settings;
  avoid printing sign-in tokens in setup output and check npm/make prerequisites.
- Add isolated first-run setup regression checks and demo tests to `make test`, plus an Arch Linux
  GitHub Actions test/build workflow. Ship the demo as source with independent Git setup instructions.
- Generate Next.js route types before UI checking so `make test` works before the first UI build.

### Maintenance
- Convert disabled boolean panic settings from Firecracker's guest config to numeric zero when
  building newer kernels that define them as integers, avoiding invalid-value Kconfig warnings.
- Correct the web panel documentation and project map after the Go agent/CLI migration:
  the daemon owns authentication/API/WebSockets on port 4100, and Next.js is the internal UI
  renderer on port 4110. Preserve the bash debugging/maintenance CLI and the UI Go-module boundary.

### k3s deployment demonstration
- Add an independent local Git repository in `examples/k3s-demo`: tested Go HTTP application,
  static non-root container, image build/push pipeline, two-replica Kubernetes deployment with
  probes/resource limits, and a hostname-scoped ingress on the existing local k3s port.
- Document namespace-scoped protected deploy credentials, repeat deployments and demo removal.

### Protected secrets, Go CLI and regression coverage
- Protect existing/new secrets by default; add per-project branch globs, write-only secret
  protection metadata and panel settings. Tags/detached commits cannot inherit protected-branch
  access, and reruns retain canonical source refs. Explicitly unprotected values remain supported.
- Replace the Node CI client with Go, preserving commands, API authentication, flags and exit
  semantics; add `--protected`/`--unprotected`. Node remains a UI dependency only.
- Add checked-in API golden JSON (`go test ./internal/api -run TestContract -update`), auth/guard/
  webhook checks, fake-executor scheduler tests and an executable disposable-pipeline e2e script.
- Cap rendered job logs at `CI_MAX_LOG_MB` (default 50 MiB), emitting one truncation notice while
  preserving step parsing. Bound guest/agent payload spools too, retaining post-limit control records.

### Changes — fcagent phases 2–5
- Move lab/status, reaping, panel inspection/exec and the CI executor to HTTP/JSON over a private
  Unix socket forwarded through the existing pinned SSH master. No new network ports.
- Implement VM spawn/removal with firecracker-go-sdk and netlink, retaining routed /30 addresses,
  IP-derived MACs, sparse rootfs copies, disk growth and existing guest firewall isolation.
- Implement binary-safe streaming uploads/exec/downloads and image builds with clean rootfs publication.
- Agent-owned detached runners keep raw output and stream it by job ID/offset without per-second
  SSH polling. Daemon restarts recover the same attempt, log and step state, then collect caches and
  artifacts. Private atomic checkpoints retain cache hashes and encrypted masking snapshots.
- Keep cancellation/timeouts agent-side; persist terminal job state before deleting VM/job files.
  Jobs interrupted before ownership transfers (during preparation) still fail explicitly.
- Remove executor fcctl paths; retain the bash CLI for debugging, interactive terminals and maintenance.
- Wait for outer-VM provisioning to finish before scheduling; protect agent-owned operations from
  stale reaping while still removing abandoned image-build VMs.

### Changes — fcagent phase 1
- Add a static Linux/x86_64 Go agent in the outer VM, running as root under systemd with a
  root:lab 0660 Unix socket (`/run/fcagent.sock`) and no TCP listener. Read-only HTTP/JSON endpoints
  reproduce fcctl host/status, VM list, images, serial console and Firecracker API queries.
- Build the agent on the host (local Go or Docker), upload it alongside the kernel, and install
  and health-check it during guest provisioning. Keep fcctl and all current daemon callers intact.
- Add tests for JSON shapes/nullability, VM addressing/order/state/metrics, sparse disk accounting,
  console tails, Firecracker forwarding/errors, input validation and Unix socket lifecycle/access mode.
- This phase does not yet recover jobs after daemon restarts; stop for review before phase 2.

### Changes — Go backend
- New daemon **`spikeloaderd` (Go)**: HTTP API, sign-in, CI/CD engine (pipeline parser, triggers,
  queue/scheduler, executor, git mirrors, outer VM lifecycle), PostgreSQL, encrypted secrets,
  Kubernetes view and the terminal WebSocket. Same API contract, data format and secrets key as before:
  existing projects, runs and secrets are read as they are.
- `web/` is now the UI only (no API routes, custom server or server libraries); the daemon supervises
  `next start` (or `next dev`) on 127.0.0.1:4110 and proxies it on port 4100.
- `spikeloader web start` builds the daemon when Go sources changed (local Go, or the `golang` Docker
  image when Go is not installed) and runs it; new `spikeloader web dev`, `spikeloader build`,
  `make build`, `make test`. The systemd unit runs the daemon.
- Docs for continuing the work: `docs/backend.md` (architecture, contract, invariants),
  `docs/roadmap.md`, `.github/copilot-instructions.md`, `AGENTS.md`.
- Fixes found while porting: `spikeloader ci rm` / `ci fetch` were rejected (no JSON content type);
  a project deleted while one of its jobs was finishing could be written back to the database;
  `fcctl status` had no hostname on Arch (no `hostname` command).

### Security
- MicroVM isolation in the outer VM: a job can no longer reach another job's microVM, the outer VM's
  own services (sshd) or QEMU's user-network addresses (`10.0.2.2` is the host's loopback with the
  native backend). Internet and host services on the Docker bridge (k3s, registry) stay reachable.
- Job caches are scoped by branch (read fallback: default branch), so a branch cannot poison the
  cache the default branch and its deploys restore.
- Secrets are encrypted at rest (AES-256-GCM, key in `.lab/secrets.key`; back it up with the database).
- SSH to the outer VM pins its host key on first use (`.lab/known_hosts`, reset with the disk) instead
  of `StrictHostKeyChecking=no`; the multiplexing socket moved from `/tmp` to `$XDG_RUNTIME_DIR`
  (or a checked 0700 directory).
- The panel address comes from `SPIKELOADER_HOST`/`SPIKELOADER_PORT` (no longer the generic
  `HOST`/`PORT`, which zsh sets to the machine name); `SPIKELOADER_AUTH=off` is refused off loopback
  and a network-facing address prints a warning. The CI CLI reads the same variables.
- Only the exact `/login`, `/api/auth/login` and `/api/health` paths skip authentication.
- Security headers on every response (no framing, `nosniff`, `no-referrer`).
- git only uses http(s)/ssh/git/file transports (`GIT_ALLOW_PROTOCOL`).
- `make up` no longer prints the sign-in link (it contains the token): use `spikeloader web url`.

### Changes — configuration file
- `lab.env` is now `.env` (same format; also inside the outer VM, where the old copy is removed).

### Changes — self-hosted bootstrap
- `bootstrap.sh` (replaces `install.sh`; also `make bootstrap`): prerequisites, panel, CLI, database
  and microVM kernel in one idempotent run; `--k3s` adds a local Kubernetes + registry
  (`host/k3s.sh`, images pinned by `K3S_IMAGE`/`REGISTRY_IMAGE`), `--service` the systemd unit.
- `make k3s` also starts the registry and refreshes the kubeconfig; new `make k3s-reset`.
- `doctor` requires Docker and git, and reports whether the microVM kernel is built.

### Changes — Arch Linux everywhere, mainline microVM kernel
- The outer VM runs the Arch Linux cloud image (`OUTER_IMG_URL`, replaces `UBUNTU_IMG_URL`); changing
  the URL re-downloads the image and recreates the disk.
- MicroVMs boot Arch Linux (rootfs built with pacstrap from `GUEST_PACKAGES`) on a kernel.org kernel
  (`GUEST_KERNEL`, default 7.3) built once on the host by `host/kernel.sh` from Firecracker's 6.18
  guest config plus extras (iptables raw table, nftables masquerade, btrfs/xfs…); `FC_CI` is gone.
- Built-in CI images use pacman. **Repository images (`.spikeloader/images/*.sh`) that call `apt-get`
  must be ported to `pacman -S --noconfirm`.**
- The Docker image no longer needs the iptables-legacy / no-raw-table workarounds.
- The QEMU container of the docker backend is based on `archlinux`.

### Changes — PostgreSQL storage
- Projects, secrets, runs (with the run id sequence) and the panel event history are stored in
  PostgreSQL instead of JSON files under `.lab/`. The database runs as the Docker container
  `spikeloader-postgres` (volume `spikeloader-pgdata`, 127.0.0.1:`PG_PORT`), started by
  `spikeloader web start`, the systemd service and `make up`; new `spikeloader db start|stop|status|psql`.
- Logs, artifacts, caches and git mirrors stay on disk in `.lab/ci/`.
- `make up` brings up the test k3s, the database and the panel; Infrastructure page grid aligned.

### Changes — Kubernetes on the Infrastructure page
- New **Kubernetes** section (read-only): cluster version and endpoint, workloads (Deployments,
  StatefulSets, DaemonSets) with rollout health, pod counts, unhealthy pods, recent Warning events,
  per-node CPU/memory (metrics-server) and namespaces. The cluster comes from `K8S_KUBECONFIG` in
  lab.env (token or client-certificate kubeconfigs; the panel calls the API directly, no kubectl).
- Images page lists the definitions while the outer VM is off instead of showing an SSH error.
- Infrastructure: the Maintenance card (shut down / delete disk) and `POST /api/outer` are gone; use
  `make down` / `make purge` (or `spikeloader down [--purge]`) from the terminal.

### Fixes
- lab.env keys with digits (e.g. `K8S_KUBECONFIG`) were ignored by the panel.

### Changes — infrastructure managed by the CI/CD
- No more manual VMs: `spikeloader up`, `vm create/start/stop/restart/pause/resume/rm`, `test` and the
  panel's create/lifecycle buttons are gone; `/api/vms` (create/delete) and `/api/vms/:id` (actions) removed.
- The engine starts the outer VM when a job is queued and stops it after `CI_IDLE_SHUTDOWN_MIN`
  minutes idle (default 30).
- Capacity-aware scheduling: jobs start while their vCPUs/memory fit in the outer VM
  (`CI_MAX_JOBS=0` = no fixed cap, the new default); jobs bigger than the VM fail with a reason.
- Panel redesign: Pipelines is the home (Projects, Runs, Validate, Images & templates, Global secrets);
  project pages with tabs; run page with stage graph, step list, line-numbered log and raw download;
  **Infrastructure** replaces Overview / Outer VM / Activity; colored status chips, icons, empty
  states, mobile layout.
- Images page tells current builds from outdated ones (safe to delete).

### Fixes
- Stale git lock files left by an interrupted fetch no longer break a project's syncs.
- Opening an access link on an already loaded login page signs in.

### New — CI/CD
- Pipelines in `.spikeloader.yml`: stages, `needs` DAG, matrix, variables, secrets, cache, artifacts,
  `when` (branches, events, status), manual approval, retries, timeouts, `allow_failure`.
- Every job runs in a throwaway microVM booted from an image built from a shell script
  (`ci/images/`: base, node, python, go, docker, security); repositories can add or override images
  and templates in `.spikeloader/`.
- Templates (`uses:`): `scanners/trivy`, `scanners/gitleaks`, `scanners/semgrep`, `docker/build`, `deploy/ssh`.
- Triggers: polling of any git URL, generic webhook, cron schedules and manual runs.
- Panel: **Pipelines** (projects, runs, stage graph, live logs, approvals, artifacts, secrets,
  pipeline validator, images); CLI: `spikeloader ci …`; API with bearer-token auth.
- Notifications to Slack/Discord/any webhook; retention of the newest `CI_KEEP_RUNS` runs.
- `fcctl`: `spawn`, `follow`, `image-build`, `images`, `image-rm`; `create` takes an image and a disk size;
  id allocation is locked so parallel creations never collide.

### Fixes
- MicroVM network throughput: GRO is disabled on the TAP devices (TCP downloads were ~80 KB/s under nested KVM).

### Changes
- The whole project is now in English: CLI, `fcctl`, host/guest scripts, web panel, docs and brand assets.
- `spikeloader status --json`: `outer.state` is now `off` | `starting` | `online` (was `desligada` | `iniciando` | `online`).

## 0.1.0 — 2026-10-22

First release of **spikeloader**, grown out of the Firecracker lab.

### New
- `spikeloader` CLI (`up`, `down`, `status`, `vm …`, `web …`, `doctor`, `test`, `version`).
- `install.sh`: sudo-free install into `~/.local/bin`, with an optional systemd service (`--service`).
- Branded web panel with token sign-in, access link (`spikeloader web url`) and installable PWA.
- `/api/health` for monitoring; version shown in the panel and the CLI.
- Panel operation history persisted in `.lab/jobs.json`.
- Visual identity: icon, favicon, app icons and banner (`brand/`).
- `fcctl`: `add`, `start`, `restart`, `pause`, `resume`, `stop-all`, `api`, `status`; `ls` shows paused VMs.

### Changes
- The outer VM is now named `spikeloader-host`.
- Clean shutdown: microVMs via Ctrl+Alt+Del and the outer VM via `poweroff` before forcing.
- Firecracker log split from the serial console (`firecracker.log`).
- Panel `FCLAB_*` variables renamed to `SPIKELOADER_*` (the old names are still accepted).

### Security
- Every page, route and the WebSocket terminal require a session (HttpOnly/SameSite=Strict cookie, HMAC of the token).
- Rotating the token invalidates every session.
- The CLI and installer refuse to run as root.
