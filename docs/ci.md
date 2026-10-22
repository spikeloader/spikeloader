# spikeloader CI/CD

Pipelines live in each repository as **`.spikeloader.yml`**. Every job boots a **fresh Firecracker
microVM** from an image, gets the code, runs its steps as root and is destroyed — no state leaks
between jobs, and a job can do anything a real machine can (Docker, systemd services, kernel
features, `pacman -S`…). MicroVMs run Arch Linux on the kernel set by `GUEST_KERNEL` in .env.

```
git push ─┐                                    ┌─ microVM (image node) ── build ──┐
webhook ──┤                                    │                                  ▼ artifacts
schedule ─┼─▶ engine (panel) ─▶ queue ─▶ DAG ──┼─ microVM (image node) ── test ×2 (matrix)
manual ───┘    fetch · parse · secrets         │
                                               └─ microVM (security) ── scan ── deploy (approval)
```

- [Quick start](#quick-start) · [Infrastructure is automatic](#infrastructure-is-automatic)
- [Pipeline reference](#pipeline-reference)
- [Images](#images) · [Templates](#templates) · [Kubernetes](#kubernetes) · [Secrets](#secrets) · [Artifacts and cache](#artifacts-and-cache)
- [Triggers](#triggers) · [Environment variables](#environment-variables) · [CLI](#cli) · [API](#api)
- [How it works](#how-it-works) · [Limits and security](#limits-and-security)

## Quick start

```bash
spikeloader web start                            # panel + CI engine (or ./bootstrap.sh --service)
spikeloader ci add my-app git@github.com:me/my-app.git --branch main
spikeloader ci run my-app -f                     # run the default branch and follow the logs
```

Or use **Pipelines** in the panel. A minimal pipeline:

```yaml
jobs:
  test:
    image: node
    steps:
      - npm ci
      - npm test
```

The first run boots the outer VM by itself and the first job that uses an image builds it (a few
minutes); later jobs boot it in a few seconds.

## Infrastructure is automatic

There is nothing to create or size by hand:

- **Outer VM** — started by the engine as soon as a job is queued and the VM is off; stopped after
  `CI_IDLE_SHUTDOWN_MIN` minutes (default 30, `0` = never) without queued or running jobs.
- **Capacity** — queued jobs start oldest first while their `vcpus` and `mem` fit in the outer VM
  (its RAM minus 1 GiB; vCPUs may be overcommitted 2×). A job bigger than the whole VM fails with a
  clear reason; `CI_MAX_JOBS` (default `0` = no cap) can limit parallelism further.
- **MicroVMs** — one per job, sized by the job, destroyed after completion or cancellation.
  Once the bundle is prepared, the Go agent owns the runner: a daemon restart reattaches to the same
  VM and attempt, reconstructs the complete masked log and collects caches/artifacts normally.

**Infrastructure** in the panel shows the state, the capacity in use, the microVM of each running
job (with a shell for debugging) and the events (automatic starts and idle shutdowns).

Provisioning installs the Go `fcagent` service (Unix socket only). The engine uses it for VM/image
lifecycle, uploads, execution and log streaming; `fcctl` remains available for debugging.
No pipeline keys change. A restart during preparation (image build, VM boot or upload), before
ownership transfers to the agent, fails that job explicitly; it can be retried. Losing the outer
VM itself is not recoverable running-job execution.

## Pipeline reference

```yaml
on:                                  # triggers (default: push to any branch + manual)
  push: { branches: [main, "release/*", "feature/**"] }
  schedule: ["0 3 * * *"]            # cron, local time, runs the default branch
  manual: true

variables: { APP: my-app }           # exported to every job

defaults:                            # job defaults
  image: base
  vcpus: 2
  mem: 2048                          # MiB
  disk: 10G
  timeout: 30m                       # s, m or h
  retry: 0
  depth: 50                          # git history depth (0 = full)
  before: [echo "runs before every job's steps"]
  after: [echo "runs after every job, even on failure"]

stages: [build, test, scan, deploy]  # order of execution (default: build, test, scan, deploy)

jobs:
  build:
    stage: build
    image: node                      # see Images
    vcpus: 4
    mem: 4096
    disk: 20G
    timeout: 20m
    variables: { NODE_ENV: production }
    steps:                           # strings or { name, run }; each step is its own bash -eo pipefail
      - npm ci
      - name: build
        run: |
          npm run build
          du -sh dist
    after: [rm -rf node_modules/.cache]
    cache:                           # see Artifacts and cache
      key: npm
      files: [package-lock.json]
      paths: [node_modules, ~/.npm]
    artifacts: [dist/]

  test:
    stage: test
    image: node
    needs: [build]                   # explicit DAG (default: every job of earlier stages)
    matrix: { node: ["20", "22"], shard: ["1", "2"] }   # 4 jobs: MATRIX_NODE, MATRIX_SHARD
    retry: 2
    allow_failure: false
    steps: [npm test -- --shard=$MATRIX_SHARD/2]

  sast:
    stage: scan
    uses: scanners/semgrep           # template; the job's own keys override it
    with: { config: p/owasp-top-ten }

  notify-failure:
    stage: deploy
    when: { status: failure }        # success (default) | failure | always
    steps: [curl -fsS -X POST "$ALERT_URL" -d "pipeline failed"]
    secrets: [ALERT_URL]

  deploy:
    stage: deploy
    manual: true                     # waits for approval in the panel / `spikeloader ci approve`
    when:
      branches: [main]               # globs: * within a segment, ** across segments
      events: [push, manual]         # push | manual | schedule | retry
    secrets: [SSH_PRIVATE_KEY]
    uses: deploy/ssh
    with: { host: app.example.com, path: /srv/app, command: "systemctl restart app" }
```

| Key | Meaning |
|-----|---------|
| `stage` | stage name (default `test`); stages run in the order of `stages:` |
| `needs` | job names this job waits for (and whose artifacts it receives); replaces the stage ordering |
| `image` | image the microVM boots (default `base`) |
| `vcpus`, `mem`, `disk` | microVM size (defaults 2 vCPU, 2048 MiB, 10G) |
| `steps`, `before`, `after` | commands; `after` always runs (cleanup, post-checks) and fails the job only if the steps before it passed; the working directory is `/workspace` |
| `matrix` | map of lists; one job per combination, values in `MATRIX_<KEY>` |
| `variables` | job variables (override pipeline variables) |
| `secrets` | secret names this job receives as environment variables |
| `cache` | list of paths, or `{ key, files, paths }` (several allowed) |
| `artifacts` | list of paths, or `{ paths, when: success\|failure\|always }` |
| `when` | `branches`, `events` and `status` conditions |
| `manual` | needs approval before starting; the run shows *awaiting approval* |
| `allow_failure` | a failure does not fail the run nor block later jobs |
| `retry` | automatic retries on failure (0-5) |
| `timeout` | per job, default `1h` |
| `uses`, `with` | template and its inputs |
| `depth` | git fetch depth for the checkout |

Unknown keys are errors, so typos are caught. Validate with `spikeloader ci lint` or in the panel.

## Images

An image is a root filesystem built from a **shell script run as root** inside a microVM:

| Image | Contents |
|-------|----------|
| `base` | Arch Linux, git, base-devel, curl, jq, rsync, python, ssh client |
| `node` | base + Node.js 22 LTS, npm, corepack (pnpm/yarn) |
| `python` | base + python, pipx, uv |
| `go` | base + latest Go |
| `docker` | base + Docker Engine, buildx and compose (started at boot; `docker run` works too) |
| `security` | base + Trivy, Gitleaks, Semgrep (with the Trivy DB pre-loaded) |
| `kube` | base + kubectl, Helm, kubeconform, `kube-login` and `kube-diagnose` |

Custom images are scripts too. In the repository, **`.spikeloader/images/NAME.sh`** adds an image
(or overrides a built-in one) for that project:

```bash
# Rust toolchain on top of base
# from: base
# size: 12G
curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal
ln -s /root/.cargo/bin/* /usr/local/bin/
```

`# from:` picks the parent image (none = plain Arch Linux rootfs; install packages with `pacman -S --noconfirm`) and `# size:` the filesystem size.
The image is identified by a hash of its script chain: change the script and the next job rebuilds
it. Built-in definitions live in [ci/images/](../ci/images/); built images can be listed and deleted
in **Pipelines → Images & templates** or with `spikeloader ci images`.

## Templates

`uses:` pulls a job definition from a template and merges the job's keys on top. Inputs declared by
the template are set with `with:` and arrive as `INPUT_<NAME>` variables.

| Template | What it does | Inputs |
|----------|--------------|--------|
| `scanners/trivy` | vulnerabilities, IaC misconfigurations and secrets; SARIF in `reports/` | `severity`, `scanners`, `exit_code` |
| `scanners/gitleaks` | secrets in the code and the fetched history | `exit_code` |
| `scanners/semgrep` | SAST with Semgrep rules | `config` |
| `docker/build` | `docker build` and optional push (login with the `REGISTRY_USER`/`REGISTRY_PASSWORD` secrets when listed) | `context`, `file`, `tag`, `push`, `registry`, `insecure_registry`, `build_args` |
| `deploy/ssh` | rsync to a server and an optional remote command (`SSH_PRIVATE_KEY` secret) | `host`, `user`, `port`, `source`, `path`, `command` |
| `deploy/kubectl` | `kubectl apply` of manifests or a kustomization, optional image override, waits for the rollout (`KUBECONFIG_DATA` secret) | `path`, `namespace`, `context`, `images`, `timeout`, `create_namespace`, `diagnose`, `rollback` |
| `deploy/helm` | `helm upgrade --install --atomic --wait` (rolls back on failure) (`KUBECONFIG_DATA` secret) | `release`, `chart`, `namespace`, `context`, `values`, `set`, `version`, `repo`, `timeout`, `diagnose` |
| `k8s/validate` | kubeconform on manifests/kustomizations, `helm lint` + kubeconform on charts (no cluster needed) | `path`, `charts`, `kubernetes_version` |

Write your own in **`.spikeloader/templates/NAME.yml`** (subdirectories allowed: `uses: team/lint`)
— same keys as a job plus `inputs:`:

```yaml
# .spikeloader/templates/lint/eslint.yml
image: node
inputs: { dir: . }
steps:
  - npm ci
  - npx eslint "$INPUT_DIR"
```

Repository templates win over the built-in ones in [ci/templates/](../ci/templates/).

## Kubernetes

Deploys to any cluster the jobs can reach (k3s, kind, EKS, GKE, AKS, on-prem): the `kube` image
has the tools and the templates do the rest.

**1. Give spikeloader a deploy account** — run once, on a machine where `kubectl` is admin on the
cluster:

```bash
ci/k8s/create-deployer.sh my-app-staging my-app-prod --server https://k8s.example.com:6443 \
  | spikeloader ci secret set my-app KUBECONFIG_DATA
```

It creates a ServiceAccount that can manage resources **only in those namespaces** (ClusterRole
`edit` bound per namespace — no cluster-wide or RBAC rights) and prints its kubeconfig. `--server`
is the API address *as seen from the jobs* (they reach the internet and your LAN through NAT).
Any kubeconfig works as `KUBECONFIG_DATA` (plain YAML or base64), but prefer a scoped one. Revoke
it with `kubectl -n my-app-staging delete serviceaccount spikeloader-deployer`.

**2. Deploy from the pipeline:**

```yaml
stages: [validate, build, staging, production]

jobs:
  manifests:
    stage: validate
    uses: k8s/validate
    with: { path: k8s/, charts: chart/my-app }

  image:
    stage: build
    uses: docker/build
    with: { tag: "ghcr.io/me/my-app:$CI_COMMIT_SHORT_SHA", push: "true", registry: ghcr.io }
    secrets: [REGISTRY_USER, REGISTRY_PASSWORD]

  staging:                       # every push to main
    stage: staging
    when: { branches: [main] }
    uses: deploy/kubectl
    with:
      path: k8s/
      namespace: my-app-staging
      images: "my-app=ghcr.io/me/my-app:$CI_COMMIT_SHORT_SHA"   # image as written in the manifests = new image

  production:                    # after staging, with approval
    stage: production
    manual: true
    when: { branches: [main] }
    uses: deploy/helm
    with:
      release: my-app
      chart: chart/my-app
      namespace: my-app-prod
      values: chart/my-app/values-prod.yaml
      set: "image.tag=$CI_COMMIT_SHORT_SHA"
```

`with:` values may use `$VAR`/`${VAR}` (CI variables, pipeline variables); nothing else is
evaluated. `deploy/kubectl` renders the path (a kustomization or a folder of manifests), replaces
the `images` *before* applying (kustomize `images:` semantics: match the image name used in the
manifests, so no pod ever starts with a placeholder), applies it, waits for every
Deployment/StatefulSet/DaemonSet it applied and, if one does not become ready, fails the job and
rolls it back to the previous revision (`rollback: "false"` to keep it);
`deploy/helm` uses `--atomic`, so a failed upgrade is rolled back. On failure both print the pods,
recent events, `describe` and logs of the unhealthy pods (`diagnose: "false"` to skip).

For your own steps, `image: kube` + `secrets: [KUBECONFIG_DATA]` and start with `kube-login`
(`kube-login <context>` to pick a context); `kube-diagnose <namespace>` prints the same diagnostics.

Private registries: create an image pull secret in the namespace once
(`kubectl create secret docker-registry …`) and reference it in your manifests or chart.

## Secrets

Set per project or globally (shared by every project), in the panel or with the CLI:

```bash
echo -n "$TOKEN" | spikeloader ci secret set my-app NPM_TOKEN
spikeloader ci secret set --global SLACK_WEBHOOK < webhook.txt
```

A job only receives the secrets it lists in `secrets:` (project values win over global ones); a
missing secret fails the job before it boots. Values are stored in the PostgreSQL database (bound
to 127.0.0.1 only), never shown again, deleted from the microVM right after they are loaded, and **masked as
`***` in logs** — the whole value and every long token inside it, so a token printed from a
kubeconfig or a JSON key is masked too.

Secrets are **protected by default**, including existing secrets migrated from older versions.
Project settings contain `protectedBranches`, a list of branch globs initially set to the default
branch (for example `["main", "release/*"]`). An empty list denies protected secrets everywhere.
Changing the default branch does not silently change an existing explicit protection policy.
A job requesting a protected secret on another branch fails with
`secret NAME is only available on protected branches`. Tags and detached commits are denied,
even if they point at the same commit as a protected branch. Reruns preserve the original ref.
Legacy stored runs without canonical branch provenance are denied protected secrets on retry.
An already agent-owned runner keeps its original secret snapshot for recovery.

To deliberately allow a non-sensitive value on every branch:

```bash
echo -n "$VALUE" | spikeloader ci secret set my-app PUBLIC_VALUE --unprotected
spikeloader ci secret ls my-app # lists names and protection flags, never values
```

`--protected` explicitly enables protection; updating a value without either flag preserves its
existing policy. New secrets default to protected. The panel can choose the flag when saving a value.
Protection applies to both project and global secrets; a project entry overrides the global entry.

### Log retention

`CI_MAX_LOG_MB` in `.env` is the maximum rendered log per job (default 50 MiB; integer 1-1024).
The log ends with one `… log truncated at N MB` notice when full. Execution continues, and
step markers are still parsed so later steps, artifacts and final job state remain correct.
The guest and agent raw spools also bound payload to this budget, retaining only a bounded
set of control records after truncation (at most one per marker kind/step).
The per-runner limit is fixed when ownership transfers; daemon configuration changes apply to
new log writers. Increasing a limit cannot recover output already discarded.

## Artifacts and cache

**Artifacts** are files a job keeps (`artifacts: [dist/, reports/]`). They are downloadable from
the run page and extracted into the workspace of every job that depends on it (`needs`, or the jobs
of later stages).

**Cache** survives between runs of the same project. The key is `key` plus a hash of the `files`
listed, so `files: [package-lock.json]` invalidates it when dependencies change. Paths may be
relative to the workspace, absolute or under `~`. Caches are restored before `steps` and saved after
a successful job. They are **scoped by branch**: a job saves only its own branch's entry and restores
it, falling back to the default branch's, so another branch can never change what the default branch
(and its deploys) restores.

## Triggers

| Trigger | How |
|---------|-----|
| push | the engine fetches every project every *poll* seconds (default 60) and starts a run for each branch that moved, if `on.push` matches |
| webhook | `POST /api/ci/hooks/<project>?token=…` (any payload — GitHub, GitLab, Gitea, Bitbucket, curl) makes it fetch right away; get the URL with `spikeloader ci hook <project>` |
| schedule | `on.schedule` cron expressions, evaluated every minute against the default branch |
| manual | **Run pipeline** in the panel or `spikeloader ci run <project> [branch\|tag\|sha]` |

The panel only listens on localhost; to receive webhooks from a hosted git service, expose the hook
path with a tunnel (the project token is the only credential it accepts), or rely on polling.

## Environment variables

| Variable | Value |
|----------|-------|
| `CI`, `SPIKELOADER` | `true` |
| `CI_PROJECT`, `CI_RUN_ID`, `CI_JOB`, `CI_JOB_ATTEMPT`, `CI_STAGE` | identifiers |
| `CI_EVENT` | `push`, `manual`, `schedule` or `retry` |
| `CI_BRANCH`, `CI_DEFAULT_BRANCH` | branch names |
| `CI_COMMIT_SHA`, `CI_COMMIT_SHORT_SHA`, `CI_COMMIT_MESSAGE`, `CI_COMMIT_AUTHOR` | commit |
| `MATRIX_<KEY>` | matrix values |
| `INPUT_<NAME>` | template inputs |

Plus pipeline `variables`, job `variables` and the job's `secrets`.

## CLI

```
spikeloader ci ls | add <name> <url> [--branch B] [--poll S] | rm <project> | hook <project> | fetch <project>
spikeloader ci run <project> [ref] [-f] | runs [project] | show <run> | logs <run> [job] [-f]
spikeloader ci cancel <run> | rerun <run> | retry <run> <job> | approve <run> <job>
spikeloader ci lint [file] | secret ls|set|rm <project|--global> [NAME] | images
```

`spikeloader ci run <project> -f` exits with 0 only if the run succeeds, so it can gate scripts.
The CI client is Go (`cmd/spikeloader`); Node is required only for the web UI. The bash launcher
builds the client beside the daemon and execs it. API location/token overrides remain
`SPIKELOADER_HOST`, `SPIKELOADER_PORT`, `SPIKELOADER_TOKEN` and `SPIKELOADER_ROOT`.
`scripts/e2e.sh` exercises the CLI and a disposable real pipeline against an already running daemon.

## API

All routes need the panel session or `Authorization: Bearer <token>` (`spikeloader web token`),
except the webhook.

| Route | |
|-------|-|
| `GET /api/ci` | projects, recent runs, queue, built-in images/templates |
| `POST /api/ci/projects` · `GET\|PATCH\|DELETE /api/ci/projects/:id` | projects |
| `POST /api/ci/projects/:id/run` `{ ref }` · `POST /api/ci/projects/:id/sync` | start a run / fetch now |
| `GET\|PUT\|DELETE /api/ci/secrets/:project\|_global` | `[{name, protected}]` / set `{name, value, protected?}` / delete `{name}` |
| `GET /api/ci/runs?project=` · `GET /api/ci/runs/:id` | runs |
| `POST /api/ci/runs/:id` `{ action: cancel\|rerun\|retry\|approve, job }` | run control |
| `GET /api/ci/runs/:id/jobs/:idx/log?offset=` · `…/artifacts` | log tail / artifacts download |
| `POST /api/ci/lint` `{ yaml, templates }` | validation |
| `GET\|DELETE /api/ci/images` | built images |
| `POST /api/ci/hooks/:id?token=` | webhook (public) |

## How it works

| Part | File |
|------|------|
| parser, templates, matrix, DAG | [internal/ci/pipeline.go](../internal/ci/pipeline.go) |
| engine: triggers, queue, run state, retention, notifications | [internal/ci/engine.go](../internal/ci/engine.go) |
| executor: images, microVM, uploads, steps, cache, artifacts | [internal/ci/executor.go](../internal/ci/executor.go) |
| git mirrors and checkouts | [internal/ci/git.go](../internal/ci/git.go) |
| microVM lifecycle, execution and images | [internal/fcagent/](../internal/fcagent/) |

For each job: the engine packs a shallow checkout of the commit (with `.git`, `origin` pointing at
the remote without credentials) once per run; the Go agent boots a copy of the image grown to
`disk`; the workspace, upstream artifacts, caches and a job bundle (env, secrets, one script per
step) are streamed in over the agent; `/ci/run.sh` runs the steps detached from the SSH session,
so steps that restart sshd do not kill the job, and its output is streamed to the job
log; caches and artifacts are streamed back; the microVM is removed.

Projects, secrets and runs live in PostgreSQL (container `spikeloader-postgres`, data in the Docker
volume `spikeloader-pgdata`; `spikeloader db psql` opens a SQL shell). Files stay in `.lab/ci/`: git
mirrors, one log and one artifacts archive per job, and caches. The newest `CI_KEEP_RUNS` runs per project are kept (default
50, in [.env](../.env)). Jobs queued while the outer VM is off start it. Agent-owned jobs survive
daemon restarts; jobs interrupted before ownership transfers fail explicitly and can be retried.

## Limits and security

- The outer VM size (`OUTER_CPUS`, `OUTER_MEM`, `OUTER_DISK` in .env) bounds how many jobs run at once.
- Firecracker runs without the jailer: the outer VM is the security boundary. Running **untrusted
  code** (e.g. pull requests from forks of a public repository) is not isolated from the other jobs
  of the outer VM — only build repositories you trust.
- Each job has internet access through the outer VM's NAT.
- Cache archives are not evicted automatically; delete `.lab/ci/cache/<project>/` to reset them.
