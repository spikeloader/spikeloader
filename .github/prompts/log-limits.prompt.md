---
mode: agent
description: Roadmap 5 — cap job log size
---
Implement **roadmap item 5** from [docs/roadmap.md](../../docs/roadmap.md): cap each job log at
`CI_MAX_LOG_MB` (new `.env` setting, default 50, documented in `.env`). In `jobLog`
(`internal/ci/executor.go`), stop writing once the limit is reached, write one line
`… log truncated at N MB`, keep parsing step markers so step states stay correct, and keep the job
running. Add a unit test for the truncation and the markers after it. Update docs/ci.md and CHANGELOG.md.
