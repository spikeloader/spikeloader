# spikeloader · web panel

The Next.js UI for spikeloader. The Go daemon owns authentication, the HTTP API,
terminal WebSockets and the CI/CD engine; this directory contains only the UI.

Run from the project root:

```bash
make up       # production: local k3s, database, daemon and UI
make web-dev  # development: database, daemon and UI hot reload
make test     # Go tests/vet, UI type check and lint
```

Open **http://127.0.0.1:4100**. Use `bin/spikeloader web url` for a sign-in link;
do not share it because it contains the access token.

The daemon supervises Next.js on **127.0.0.1:4110**, an internal renderer rather than
a second public entry point. Running `npm run dev` or `npm start` here starts only
that renderer, without the daemon's authentication/API proxy.

## How it works

```
browser ──HTTP/WS──▶ spikeloaderd :4100 ──┬─ /api/* and /ws/* handled in Go
                                        └─ pages/assets → Next.js :4110
```

| Path | Role |
|------|------|
| [app/(panel)/](app/(panel)/) | panel pages: pipelines, infrastructure and microVM inspection |
| [app/login/](app/login/) | sign-in screen |
| [components/](components/) | UI components, including the terminal and CI screens |
| [lib/types.ts](lib/types.ts), [lib/ci/types.ts](lib/ci/types.ts) | TypeScript mirrors of the Go JSON contract |
| [next.config.ts](next.config.ts) | UI headers, legacy page redirects and CSS build configuration |
| [go.mod](go.mod) | module boundary excluding UI dependencies from root Go test discovery |

Use `SPIKELOADER_HOST` / `SPIKELOADER_PORT` for the daemon bind address, not
`HOST` / `PORT`. See [backend architecture and API contract](../docs/backend.md)
for supported configuration, authentication and routes, and the
[pipeline guide](../docs/ci.md) for CI usage.
