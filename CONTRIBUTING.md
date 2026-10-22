# Contributing

Spikeloader targets Linux x86_64, Arch Linux guests, trusted repositories and one
operator per host. Start with [README.md](README.md), then
[docs/backend.md](docs/backend.md) for the architecture, concurrency and API rules.
See [docs/roadmap.md](docs/roadmap.md) before starting larger changes.

## Local development

```bash
git clone https://github.com/spikeloader/spikeloader.git
cd spikeloader
cp .env.example .env
cd web && npm ci && cd ..
make test
make build
```

Tests and builds need Go >= 1.25, Node.js >= 20.9 with npm, make, Bash and standard
Linux utilities. They do not boot VMs or require PostgreSQL, KVM or Docker when Go
is installed locally. UI builds fetch Google Fonts and need internet access.
`make web-dev` starts the database, Go daemon and hot-reloading UI; it requires Docker.

`make test` includes isolated bootstrap smoke tests, shell syntax checks, Go vet/tests,
the demo application tests, TypeScript checking and ESLint. The GitHub Actions
workflow runs tests/builds in Arch Linux; it does not claim nested-KVM end-to-end coverage.

For runtime changes, also run [scripts/e2e.sh](scripts/e2e.sh) against a disposable
installation with nested KVM, or follow the real-pipeline checks in
[docs/backend.md](docs/backend.md). Never point destructive tests at someone else's data.
Do not run a second daemon against the same database/state as a live installation.

## Pull requests

- Keep changes focused, with tests and directly related documentation.
- Keep Go JSON types and their TypeScript mirrors aligned, including null/array behavior.
- Do not add manual VM lifecycle UI/API controls; the engine owns VM lifecycle.
- Use English for code, comments, UI text, documentation and commit messages.
- Never attach `.env`, `.lab/`, kubeconfigs, sign-in links, database dumps or secret values.
  Sanitize logs before posting issues.

Contributions are accepted under [Apache-2.0](LICENSE).
Use [GitHub issues](https://github.com/spikeloader/spikeloader/issues) for reproducible
bugs and proposals; follow [SECURITY.md](SECURITY.md) for vulnerabilities.
