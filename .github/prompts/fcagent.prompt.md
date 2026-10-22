---
mode: agent
description: Roadmap 1 — replace guest/fcctl with a Go agent (fcagent) so jobs survive daemon restarts
---
Implement **roadmap item 1** from [docs/roadmap.md](../../docs/roadmap.md): the `fcagent` Go agent that
replaces `guest/fcctl` in the outer VM. Read [docs/backend.md](../../docs/backend.md) first (concurrency
rules, security invariants) and `guest/fcctl` (the behaviour to reproduce).

Work in phases. After **each** phase run `make test`, show me the result, and stop for my review:

1. **Agent skeleton**: `cmd/fcagent` (static binary, root, systemd unit, Unix socket `/run/fcagent.sock`,
   HTTP/JSON) with read-only endpoints equal to `fcctl status`, `host-info`, `json`, `images`, `console`,
   `api`. Install it from `guest/setup.sh`; build it on the host and upload it like the kernel.
2. **Client**: `internal/agent` (shared types) and a client in `internal/sshx` that reaches the socket
   through the existing SSH connection (no new port). Switch `internal/lab` and `ReapVMs` to it.
3. **Lifecycle**: `spawn`, `rm`, `image-build`, `image-rm`, `ssh` (one-shot exec). Use
   firecracker-go-sdk and netlink for taps; keep the IP-from-MAC scheme and the firewall rules.
4. **Agent-owned jobs**: the agent runs `/ci/run.sh` in the microVM and stores its output; the daemon
   streams it by job id + offset (no per-second ssh polling) and **reattaches after a daemon restart**
   instead of failing the job (`ci.New` today marks running jobs failed).
5. Remove the `fcctl` paths from the executor once everything uses the agent; keep `guest/fcctl` only as a
   debugging CLI if still useful. Update docs/backend.md, docs/roadmap.md and CHANGELOG.md.

Done when: the end-to-end check in docs/backend.md › Tests passes, and restarting `spikeloaderd` during
a long step (`sleep 60`) leaves the job running with a complete log.
