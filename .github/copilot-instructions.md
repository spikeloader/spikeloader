# Copilot instructions — spikeloader

spikeloader is a self-hosted CI/CD focused on Kubernetes deploys. Every CI job runs as root in a
throwaway **Firecracker microVM**; the microVMs live inside one **outer VM** (QEMU/KVM, Arch Linux)
that the engine starts when a job is queued and stops when idle. Single user, local machine.

The owner talks in Brazilian Portuguese; **code, comments, UI text, docs and commit messages are in
English.**

## Map

| Path | What |
|------|------|
| `cmd/spikeloaderd/` | Go daemon: API + auth + CI engine + supervises the Next.js UI (port 4100) |
| `internal/ci/` | engine: `pipeline.go` (YAML parser), `engine.go` (queue/scheduler/triggers), `executor.go` (job in a microVM), `git.go`, `store.go`, `types.go` |
| `internal/api/` | HTTP routes, guard, sign-in, webhooks, terminal WebSocket, UI reverse proxy |
| `internal/{db,jobs,lab,k8s,sshx,auth,env}` | PostgreSQL, panel events, outer VM state, Kubernetes view, SSH, token/session, `.env` |
| `web/` | Next.js 16 UI only (React 19, Tailwind 4). **No server code**: it calls the daemon's API |
| `web/lib/ci/types.ts`, `web/lib/types.ts` | TypeScript mirrors of the Go JSON types (API contract) |
| `cmd/spikeloader/` | Go CI CLI: API client, log following, pipeline lint and secrets |
| `bin/spikeloader` | operator launcher (bash): `web start|dev`, `db`, `build`, `doctor`, `ci …` (execs the Go CLI) |
| `cmd/fcagent/`, `internal/fcagent/`, `internal/agent/` | Go outer-VM agent, lifecycle/execution and shared protocol types |
| `guest/` | runs in the outer VM: `setup.sh` (provisioning, firewall), `fcagent.service`, `fcctl` (bash debugging/maintenance CLI) |
| `host/` | host scripts: `up.sh`/`down.sh` (outer VM), `kernel.sh` (microVM kernel build), `k3s.sh` |
| `ci/images/*.sh`, `ci/templates/**/*.yml` | built-in CI images (pacman, Arch) and job templates |
| `.env` | lab configuration (outer VM size, kernel version, CI limits, Kubernetes view) |
| `docs/backend.md` | **read first**: packages, concurrency rules, API contract table, security invariants |
| `docs/roadmap.md` | next work, in order, with acceptance criteria |
| `docs/ci.md` | pipeline format (`.spikeloader.yml`) for users |

## Commands

```bash
make test        # go vet + go test ./... + UI tsc + eslint — run before finishing any change
make build       # daemon (.lab/bin/spikeloaderd) + UI build
make web-dev     # daemon + UI hot reload at http://127.0.0.1:4100 (spikeloader web url → sign-in link)
make up          # k3s + PostgreSQL + daemon (production UI)
go run ./cmd/spikeloaderd      # from source; SPIKELOADER_UI=off for API-only work
```

Go is at `~/.local/go/bin` on the owner's machine (add it to PATH). Never run anything with sudo.

## Rules

1. **API contract.** Changing a JSON type means changing the Go type and its TypeScript mirror
   together. Nullable = pointer / nil in Go, `T | null` in TS. Lists that TS types as arrays must never
   be `nil` in Go (they would marshal as `null`). Stored runs/projects are jsonb of these types.
2. **Concurrency.** Engine state is guarded by `Engine.mu`; lowercase engine methods expect it held.
   No git/ssh/HTTP while holding it. The executor changes runs only via `jobContext.mutate`.
3. **Security invariants** (details in docs/backend.md): sign-in on everything but `public()` paths;
   Host/Origin/JSON guard on API routes; remote commands built with `sshx.Command`/`Quote`; job code
   runs only inside microVMs; secrets encrypted at rest and masked in logs; caches scoped by branch;
   names that become paths are validated; `SPIKELOADER_AUTH=off` only on loopback; never read the bind
   address from `HOST`/`PORT`.
4. **No manual VM lifecycle.** The engine owns the outer VM and the microVMs. Do not add UI buttons or
   API routes to create/start/stop them (`make down`/`make purge` in the terminal are the only manual
   controls).
5. **Arch Linux everywhere** (outer VM, microVM rootfs via pacstrap, CI images via pacman). Do not
   reintroduce Ubuntu/apt. The microVM kernel is built from kernel.org (`GUEST_KERNEL`, mainline by
   the owner's choice) by `host/kernel.sh`.
6. **Kubernetes is the core use case**: deploy templates live in `ci/templates/deploy/`, the cluster
   view in `internal/k8s` (read-only, kubeconfig with token/client cert; no exec plugins).
7. Match the surrounding style: small packages, doc comments that explain *why*, errors returned (not
   panics), `ci.Error{Msg, Status}` for errors the API should map to an HTTP status.
8. Update docs with the code: `docs/backend.md` (contract table, packages), `docs/ci.md` (pipeline
   keys), `README.md`, and an entry under `## Unreleased` in `CHANGELOG.md`.

## Prompt files (Copilot Chat: type `/` and the name)

| Prompt | Task |
|--------|------|
| `/fcagent` | roadmap 1 — Go agent replacing `guest/fcctl`, jobs survive daemon restarts (phased) |
| `/protected-secrets` | roadmap 2 — secrets only on protected branches |
| `/go-cli` | roadmap 3 — CI CLI in Go |
| `/contract-tests` | roadmap 4 — golden JSON, guard and engine tests, e2e script |
| `/log-limits` | roadmap 5 — cap job log size |
| `/verify` | check any change: `make test` + a real pipeline run on a spare port |

They live in `.github/prompts/`; each points at its roadmap section and says when it is done.

## Verifying a change end to end

```bash
make test
spikeloader web start            # or make web-dev
# a throwaway repo with a pipeline:
git init /tmp/demo && cd /tmp/demo && printf 'jobs:\n  hello:\n    steps: [uname -a]\n' > .spikeloader.yml \
  && git add -A && git commit -qm demo
spikeloader ci add demo file:///tmp/demo && spikeloader ci run demo -f
spikeloader ci rm demo
```

The first job boots and provisions the outer VM (minutes the first time) and builds the `base`
image. Check the panel pages you touched in a browser.
