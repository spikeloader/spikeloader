---
mode: agent
description: Roadmap 2 — secrets only on protected branches
---
Implement **roadmap item 2** from [docs/roadmap.md](../../docs/roadmap.md) (protected branches for
secrets). Read [docs/backend.md](../../docs/backend.md) first.

- Project setting `protectedBranches` (globs; default: the default branch) in `internal/ci/types.go`
  **and** `web/lib/ci/types.ts`, editable in `PATCH /api/ci/projects/{id}` and in the project settings UI.
- `secrets` table: column `protected boolean NOT NULL DEFAULT true` (migration in `internal/db` schema
  with `ALTER TABLE … ADD COLUMN IF NOT EXISTS`); `PUT /api/ci/secrets/{scope}` accepts `protected`;
  the secrets list returns names with the flag (update the UI and `bin/spikeloader-ci.mjs` accordingly).
- In `Engine.execute`, inject a protected secret only when the run's branch matches a protected glob;
  otherwise fail the job with `secret NAME is only available on protected branches`.
- Unit tests for the matching and for the failure message. Update docs/ci.md (Secrets section),
  docs/backend.md (contract table) and CHANGELOG.md. Run `make test`.
