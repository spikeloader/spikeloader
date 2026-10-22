---
mode: agent
description: Roadmap 4 — tests that guard the API contract, the engine and the guard
---
Implement **roadmap item 4** from [docs/roadmap.md](../../docs/roadmap.md):

1. Golden-JSON tests in `internal/api` for every response type in the contract table of
   [docs/backend.md](../../docs/backend.md) (`testdata/*.golden.json`, `-update` flag to regenerate). A
   list field becoming `null` or a renamed field must fail.
2. `httptest` tests for sign-in, `public()` paths, Host/Origin/content-type guard and the webhook token.
3. Engine tests without a real outer VM: introduce a small interface for what the executor needs
   from `sshx` (spawn, run, stream, rm) with a fake implementation, then test DAG advance,
   `when.status`, manual approval, retries, cancel, retention and capacity/FIFO.
4. `scripts/e2e.sh` automating the demo pipeline against a running daemon (see docs/backend.md › Tests).

Keep production behaviour unchanged; `make test` must stay fast (no VM needed).
