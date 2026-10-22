# Backend (Go)

The backend is one Go binary, `spikeloaderd`, built from this repository (`go.mod` at the root,
module `spikeloader`). It replaced the TypeScript engine that used to run inside the Next.js server;
`web/` is now only the user interface.

```
browser / CLI ──HTTP──▶ spikeloaderd :4100 ──┬─ /api/*, /ws/*  handled in Go (internal/api)
                                             └─ everything else → Next.js UI :4110 (reverse proxy)
spikeloaderd ──pgx──▶ PostgreSQL (container spikeloader-postgres)
spikeloaderd ──SSH Unix forwarding──▶ fcagent /run/fcagent.sock ──SDK/netlink──▶ Firecracker microVMs
spikeloaderd ──HTTPS──▶ Kubernetes API (read-only view, K8S_KUBECONFIG)
```

## Running it

| Command | What it does |
|---------|--------------|
| `make up` / `spikeloader web start` | builds what changed (daemon + UI), starts the database, runs the daemon in the foreground |
| `make web-dev` / `spikeloader web dev` | same, with the UI in `next dev` (hot reload). Go changes need a restart (it rebuilds) |
| `make build` / `spikeloader build` | builds `.lab/bin/spikeloaderd`, the Go CLI `.lab/bin/spikeloader`, the static guest agent `.lab/bin/fcagent` (local Go, or the `golang` Docker image), and the UI |
| `make test` | `go vet` + `go test ./...`, then the UI type check and lint |
| `go run ./cmd/spikeloaderd` | run from source (set `SPIKELOADER_UI=off` to work on the API alone) |

The public checkout ships `.env.example`, not `.env` or `.lab/`. Bootstrap copies
the example to a private local `.env` only when missing; existing configuration is preserved.
Host scripts report an explicit setup instruction when configuration is absent.
Before `olddefconfig`, `host/kernel.sh` converts Firecracker's disabled soft-lockup/hung-task panic
settings to zero only when the target kernel defines them as integers. Older boolean definitions
and explicitly configured values are preserved.
`make test` also validates isolated first-run setup, shell syntax and the standalone demo.
The GitHub Actions workflow runs tests/builds in Arch Linux without starting VMs, with an init
process to reap detached test runners so `tail --pid` detects their exit.

Environment of the daemon: `SPIKELOADER_HOST`/`SPIKELOADER_PORT` (listen, default 127.0.0.1:4100),
`SPIKELOADER_UI` (`prod` default = `next start`, `dev` = `next dev`, `off`, or an `http://` URL of a UI
you run yourself), `SPIKELOADER_UI_PORT` (4110), `SPIKELOADER_AUTH=off` (refused off loopback),
`SPIKELOADER_ROOT`, `DATABASE_URL`. Configuration of the lab itself is `.env` at the repository root,
re-read on every use (`internal/env`).

## Packages

| Package | Responsibility |
|---------|----------------|
| `cmd/spikeloaderd` | entry point: checks, database, engine, UI supervision (restarts `next` with backoff), HTTP server, graceful shutdown |
| `cmd/spikeloader` | Go CI CLI: API client, log following, lint with local templates and write-only secret management |
| `cmd/fcagent`, `internal/fcagent` | root service in the outer VM: inspection, SDK/netlink lifecycle, exec, image builds and agent-owned jobs over `/run/fcagent.sock` |
| `internal/agent` | agent JSON types, matching the existing host/microVM/image shapes; host and VM types mirror `web/lib/types.ts` |
| `internal/env` | finds the checkout (`Root`), `.lab/` (`State`), parses `.env` (same rules as the shell scripts) |
| `internal/auth` | access token (`.lab/web-token`), session cookie = HMAC-SHA256(token, "spikeloader-session-v1"), bearer for the CLI |
| `internal/sshx` | pinned/multiplexed SSH, private Unix forwarding, agent HTTP/JSON client, binary exec frames and resumable log streams |
| `internal/db` | pgx pool, schema (`CREATE … IF NOT EXISTS`), **ordered background writer** (`db.Write`) |
| `internal/jobs` | panel operations (start/stop outer VM) and their history (`panel_jobs`) |
| `internal/lab` | outer VM state and `GET /api/status` |
| `internal/k8s` | read-only cluster snapshot from the kubeconfig (no client-go, no kubectl) |
| `internal/ci` | the CI/CD engine (below) |
| `internal/loglimit` | validated log budget and exact bounded rendered writer with a single truncation notice |
| `internal/api` | routes, guard, sign-in, webhooks, terminal WebSocket, UI proxy |

### internal/ci

| File | Content |
|------|---------|
| `types.go` | run/job/project types. **Their JSON is the API contract and the stored format** |
| `pipeline.go` | `.spikeloader.yml` parser (ordered YAML, templates `uses:`/`with:`, matrix, DAG validation) |
| `cron.go` | 5-field cron matcher |
| `git.go` | bare mirrors, fetch, `readAt`/`listAt`, workspace packing (`GIT_ALLOW_PROTOCOL` restricted) |
| `store.go` | paths under `.lab/ci/`, PostgreSQL records, secret encryption (AES-256-GCM, `.lab/secrets.key`) |
| `executor.go` | one job in a microVM: image build, spawn, uploads, bundle, steps, caches (scoped by branch), artifacts; masked job log |
| `recovery.go` | atomic private job checkpoint: agent identity, VM, log prefix, cache hashes and encrypted masking snapshot |
| `protected.go`, `runtime.go` | protected-branch secret selection and executor/run-storage boundaries for isolated engine tests |
| `engine.go` | projects, secrets, triggers (poll, webhook, schedule, manual), queue/scheduler, capacity, outer VM lifecycle, retention, notifications |

## Concurrency model

The engine keeps its working set in memory (`Engine.projects`, `runs`, `secrets`) behind one mutex
`Engine.mu`. Rules:

1. Every read or write of engine state holds `mu`. Lowercase methods (`tick`, `advance`, `start`,
   `finished`, `persist`, `view`, `listRuns`…) **expect `mu` held**; exported ones take it.
2. Slow work — git, agent/SSH, image builds, HTTP notifications — runs **without** `mu`, then applies
   its result under it (see `Sync`, `createRun`, `execute`).
3. The executor never touches a run directly: it goes through `jobContext.mutate(func())`, which
   takes `mu`, applies the change and schedules a save.
4. Lock order: `jobLog.mu` → `Engine.mu` (the log parser reports step markers). Never the reverse.
5. Persisting marshals the run **under `mu`** (a consistent snapshot) and queues the SQL; `db.Write`
   applies statements in order on one goroutine. `persist(r, false)` debounces 500 ms (step
   progress); `persist(r, true)` saves now (status changes).

Loops: `tick` every second (advance runs, start queued jobs while they fit in the outer VM's capacity,
FIFO), `housekeeping` every 10 s (outer VM health and start/idle-stop, reaping leftover microVMs once,
polling remotes, cron schedules).

After the job bundle is prepared, the agent owns the runner independently of the daemon.
On daemon restart, running jobs with recovery checkpoints reattach to the same VM/attempt;
jobs interrupted during image/VM/workspace preparation (before ownership transfers) fail explicitly
and can be retried. The reaper preserves active recovered VMs and removes abandoned CI VMs,
including abandoned image builders. Conditional removal checks both the VMM identity and
agent-owned preparation, image builds and active tasks.

## Outer VM agent

`host/agent.sh` builds `.lab/bin/fcagent` with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`,
using local Go or the same Docker toolchain as the daemon. `make build` includes it; `host/up.sh`
builds/uploads it alongside the kernel. `guest/setup.sh` installs the binary and
`guest/fcagent.service`, enables the service and restarts it only when changed or inactive.
An HTTP readiness check must pass before provisioning continues. The scheduler also waits for
the entire outer-VM provisioning operation to finish: SSH and an older running agent alone
are not sufficient readiness while installation may still restart the service.

The service runs as **root**, with primary group `lab`. Its only listener is
`/run/fcagent.sock` (root:lab, **0660**); no TCP port or new public daemon route is added.
The executable refuses non-root execution. Shutdown removes its socket; startup recovers a stale
socket but refuses to replace a regular file or an active listener.

| Agent GET path | Equivalent / response |
|----------------|-----------------------|
| `/host-info` | `fcctl host-info`: `HostInfo` |
| `/vms` (alias `/json`) | `fcctl json`: `MicroVM[]`, sorted numerically, never null |
| `/status` | `fcctl status`: `{host: HostInfo, vms: MicroVM[]}` |
| `/images` | `fcctl images`: built image array, never null |
| `/vms/{id}/console?lines=` | `fcctl console`: `{text}`; all lines by default, explicit count 1–5000 |
| `/vms/{id}/api?path=` | `fcctl api`: raw Firecracker JSON, default `/vm/config`, GET only |

| Agent mutating path | Request / response |
|---------------------|--------------------|
| `POST /vms` | `{vcpus, mem_mib, image, disk, label}` / `{id}`; SDK starts the VMM, netlink sets up the TAP |
| `DELETE /vms/{id}` | cancel agent jobs, remove VM, TAP, disk and private agent job files |
| `POST /vms/{id}/exec?command=` | raw stdin body / NDJSON binary stdout/stderr frames and final exit code |
| `POST /images` | `{name, from, size, script}` / NDJSON build output and final exit code |
| `DELETE /images/{name}` | remove a built image; missing is idempotent |
| `POST /jobs/{job}` | `{vm, timeoutS}` / `JobState`; stable identity makes start idempotent |
| `GET /jobs/{job}` | job identity, VM, deadline and nullable exit code |
| `GET /jobs/{job}/log?offset=` | NDJSON `{data, offset}` chunks (base64 bytes), terminated by `{offset, code}` |
| `DELETE /jobs/{job}` | cancel the runner; final exit code 130 (timeout is 124) |

VM IDs are 1–16000; API paths follow fcctl's `^/[a-z/_-]*$` validation and are sent only
to that VM's Unix socket. Errors are `{"error":"message"}` with 400 (invalid parameters),
404 (missing state/file), 409 (stopped VM), 502 (Firecracker unavailable/failed), or 500.
Inspection errors are logged and fail the request rather than returning incomplete success-shaped
snapshots. Process metrics use the same `ps` fields as fcctl; disk usage counts allocated blocks.

The daemon's lab queries, reaper, executor and panel inspection/one-shot exec use the agent.
OpenSSH `-O forward`/`-O cancel` manages a private local Unix socket on the existing pinned master;
there is no TCP listener. `guest/fcctl` remains only for operator debugging/interactive terminals
and maintenance shutdown; the executor has no fcctl calls.

The agent launches a detached runner inside the microVM, then streams its file through one SSH
session (`tail --pid`), reconnecting at its saved byte offset if sshd restarts. No per-second SSH
polling remains. Agent output and job state are private files under `/var/lib/fclab/jobs/`.
Log subscribers wait for write notifications, can detach freely, and reattach by identity/offset.
Timeout/cancel terminates the detached process group; successful jobs collect caches/artifacts
before VM cleanup. Image builds use the same runner and publish their rootfs only after clean shutdown.

The daemon writes a private checkpoint before transferring ownership. After a crash it truncates
the local rendered log to the saved preparation prefix and replays the raw agent output, rebuilding
step state without duplicate log lines. Masking values are snapshotted with the existing AES-GCM key,
so changing/deleting a secret while the daemon is down cannot expose its old value during replay.
Cache hashes and the original start time also survive. Checkpoints contain no plaintext secrets and
do not change public Run/Job JSON. The engine persists the final status before removing the VM.
Running-job recovery requires the outer VM and its disk to remain available; a missing VM or corrupt
checkpoint is an explicit failure, not a new job attempt.

## API contract

The UI (`web/`) and the Go CLI (`cmd/spikeloader`) depend on these exact paths and JSON shapes.
TypeScript mirrors: `web/lib/ci/types.ts` (CI) and `web/lib/types.ts` (lab, jobs, Kubernetes).

| Method and path | Body / query | Response |
|-----------------|--------------|----------|
| `POST /api/auth/login` | `{token}` | sets the session cookie |
| `POST /api/auth/logout` | `{}` | clears it |
| `GET /api/health` | — (public) | `{ok, service, version, uptime_s, backend}` |
| `GET /api/status` | — | `LabStatus` |
| `GET /api/jobs`, `GET /api/jobs/{id}` | — | `Job[]`, `Job` |
| `GET /api/k8s` | — | `K8sOverview` |
| `GET /api/vms/{id}/api?path=` | Firecracker GET path (whitelist) | `{path, ok, data, error}` |
| `GET /api/vms/{id}/console?lines=` | 10–5000 | `{text}` |
| `POST /api/vms/{id}/exec` | `{command}` | `{code, stdout, stderr, durationMs}` |
| `GET /api/ci` | — | `{projects, runs, stats, globalSecrets, templates, images}` |
| `POST /api/ci/lint` | `{yaml, templates?}` | `{ok, errors, on?, stages?, jobs?}` |
| `GET /api/ci/images` / `DELETE` | — / `{name}` | `{built: Image[] \| null, definitions}` / `{built}` |
| `POST /api/ci/projects` | `{name, url, defaultBranch?, pollS?}` | `ProjectView` (201) |
| `GET /api/ci/projects/{id}` | — | `{project, webhookToken, secrets, runs}` |
| `PATCH /api/ci/projects/{id}` | `ProjectPatch` | `ProjectView` |
| `DELETE /api/ci/projects/{id}` | `{}` | `{ok}` |
| `POST /api/ci/projects/{id}/run` | `{ref?}` | `RunSummary` (201) |
| `POST /api/ci/projects/{id}/sync` | `{}` | `ProjectView` |
| `GET/PUT/DELETE /api/ci/secrets/{scope}` | — / `{name, value, protected?}` / `{name}` | `SecretInfo[]` (`{name, protected}`), never values |
| `GET /api/ci/runs?project=&limit=` | — | `RunSummary[]` |
| `GET /api/ci/runs/{id}` | — | `Run` (with job specs) |
| `POST /api/ci/runs/{id}` | `{action: cancel\|rerun\|retry\|approve, job?}` | `RunSummary` |
| `GET /api/ci/runs/{id}/jobs/{job}/log?offset=` (`&raw=1`) | — | `{text, offset, reset, done, status}` (or the file) |
| `GET /api/ci/runs/{id}/jobs/{job}/artifacts` | — | tar.gz |
| `POST /api/ci/hooks/{id}` | token in `?token=`, `X-Spikeloader-Token`, `X-Gitlab-Token` or bearer (public) | 202 |
| `GET /ws/terminal?target=outer\|<id>&cols=&rows=` | client sends `{t:"i",d}` / `{t:"r",c,r}` | text frames (valid UTF-8) |

Projects and project views expose `protectedBranches: string[]`; omitted legacy settings normalize
to `[defaultBranch]`, while `[]` denies protected secrets on all branches. The project POST and PATCH
accept this setting. Secret storage adds `protected boolean NOT NULL DEFAULT true`, migrating
existing secrets fail-closed. PUT defaults new secrets to protected and preserves an existing flag
when omitted. Run and RunSummary expose `sourceRef`, the canonical branch ref captured by the trigger;
detached/tag and legacy runs have an empty value, not an inferred trusted branch. Reruns preserve it.
The executor applies current branch policy before injecting any new protected secrets; already
agent-owned recovery uses the original encrypted snapshot instead.

`CI_MAX_LOG_MB` bounds the rendered log, including its single truncation notice. The raw guest/agent
spools retain at most the payload budget plus bounded step control records; the detached runner
continues draining output, preserving its exit status and later step markers. The guest's
byte-limited `head` runs unbuffered so output without a trailing newline streams immediately.

Errors are `{"error": "message"}` with the status from `ci.Error` (400/401/404/409/502) or 500.

**Changing a type:** edit `internal/ci/types.go` (or the package's type) **and** its TypeScript
mirror in the same change. Nullable fields are pointers or nil slices/maps in Go and `T | null` in
TypeScript; fields that are lists in TypeScript must never be `nil` in Go (`nonNil`, `[]T{}`), or
they marshal as `null` and break the UI. Stored runs are decoded with the same types, so renaming a
JSON field needs a migration of the `runs.data` / `projects.data` jsonb.

## Security invariants (do not regress)

- Every `/api/*` and `/ws/*` request needs a session or bearer, except `public()` paths in
  `internal/api/api.go`. Pages without a session redirect to `/login`.
- Guarded routes check `Host` (DNS rebinding); mutating ones also `Origin` and a JSON content type
  (CSRF). The terminal checks `Origin` on the upgrade.
- `SPIKELOADER_AUTH=off` only on loopback; the bind address never comes from `HOST`/`PORT`.
- Shell commands sent to the outer VM go through `sshx.Command`/`Quote` (each argument literal).
  Agent requests are JSON/binary over the SSH-forwarded Unix socket. Job commands are interpreted
  **inside the microVM** only; the agent never executes pipeline shell on the outer VM.
- Secrets: encrypted at rest, written to the microVM in a 0600 file that the runner deletes after
  loading, masked in logs (whole value and long tokens inside it).
- Caches are per branch (read fallback: default branch); workspaces never contain credentials.
- Image and template names are validated (`imageRe`, `templateRe` without `..`): they become paths.
- git: `GIT_ALLOW_PROTOCOL=https:http:ssh:git:file`, no prompts.
- The outer VM isolates microVMs from each other, from itself and from QEMU's `10.0.2.0/24`
  (`guest/setup.sh`).

## Adding things

- **An endpoint:** add the route in `internal/api/routes.go` with `s.handle("METHOD /path", mutating, …)`
  (guarded) — use `respond(w, status, value, err)` so engine errors map to their status. Add the
  TypeScript type if the UI uses it, and a row in the table above.
- **Engine behaviour:** keep the concurrency rules; add a unit test next to the code
  (`pipeline_test.go`, `store_test.go` are examples). Parser changes must keep the existing tests green —
  they pin the semantics the TypeScript version had.
- **A pipeline key:** `jobKeys`/`templateKeys` in `pipeline.go`, the `JobSpec` field, the executor,
  [docs/ci.md](ci.md) and the TypeScript type.
- **A `.env` setting:** document it in `.env` itself; read it with `env.Get`/`env.Load` at use time.

## Tests

`make test` requires no outer VM or database. API snapshots in `internal/api/testdata/*.golden.json`
cover response types and deterministic route responses, including empty arrays and nullable fields.
Regenerate only after reviewing a contract change:
`go test ./internal/api -run TestContract -update`.
Route tests check cookie/bearer sign-in, token rotation, public paths, Host/Origin/JSON guards,
webhook denial and malformed policy updates.
Engine tests inject a fake job executor and in-memory run store; they cover DAG rules,
failure/always/allowFailure, approvals, automatic/downstream retries, cancellation, FIFO capacity,
oversized jobs, retention and protected-secret denial without spawning a VM.
CLI HTTP fixtures cover command requests, headers, output and failure paths.

For real acceptance against an already running spare-port daemon:
`SPIKELOADER_PORT=4102 scripts/e2e.sh`. It creates and removes only its disposable repo/project,
checks artifacts/matrix/cache/masking, protected branch vs branch/tag/detached commit access,
explicitly unprotected values and the operator CLI commands.

`go test ./...` covers the pipeline parser (job order, defaults, matrix, templates, errors, JSON
nullability), cron, globs, durations, and secret encryption (including a vector written by the
former TypeScript code), plus fcagent inspection JSON, sparse disk usage, VM state/metrics,
read-only routing, Firecracker forwarding, console tails, input errors and Unix socket lifecycle.
Agent lifecycle tests cover concurrent ID reservations, idempotent task identities, offset replay,
timeouts/cancellation, runner exit 255 and active-operation reaper protection. Client/recovery tests
cover broken-stream reconnection without duplicate bytes, checkpoint validation and replay with
the original encrypted masking snapshot.
`go test -race ./internal/ci ./internal/fcagent ./internal/sshx ./internal/env` also checks these paths
under the race detector. End-to-end checks used during the migration (worth automating, see the
roadmap): create a local git repo with a pipeline, `spikeloader ci add demo file:///…`,
`spikeloader ci run demo -f`, and verify images build, artifacts flow between stages, caches restore
on the second run, secrets are masked, and runs/secrets survive a daemon restart. From a cold start,
assert the first job starts only after the outer provisioning operation finishes.

For recovery, emit 50 numbered lines before and after a `sleep 60`, kill only the test daemon with
SIGKILL during the sleep, and restart it on the same spare port. Check the agent task remains running,
the VMM PID/VM ID and attempt stay unchanged, and the finished log contains each numbered line exactly
once. The downstream artifact/matrix jobs must still succeed. Separately verify a real timeout
returns 124, cancellation stops the runner, and panel inspection/console/exec retain their response
shapes.
