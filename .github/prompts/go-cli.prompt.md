---
mode: agent
description: Roadmap 3 — port the CI CLI (bin/spikeloader-ci.mjs) to Go
---
Implement **roadmap item 3** from [docs/roadmap.md](../../docs/roadmap.md). Create `cmd/spikeloader`
reproducing every `ci` subcommand of `bin/spikeloader-ci.mjs` with the same arguments, output and exit
codes (read the .mjs file completely first). It talks to the daemon API with the bearer token from
`.lab/web-token` (or `SPIKELOADER_TOKEN`), base URL from `SPIKELOADER_HOST`/`SPIKELOADER_PORT`; mutating
requests always send `Content-Type: application/json` (the CSRF guard requires it).

Build it in `ensure_daemon` of `bin/spikeloader` next to `spikeloaderd`, make `spikeloader ci …` exec
it, delete the `.mjs` file, and update README.md, docs/ci.md and CHANGELOG.md. Test with a throwaway
repo: `ci add`, `ci run -f`, `ci runs`, `ci show`, `ci logs`, `ci secret set|ls|rm`, `ci lint`, `ci rm`.
