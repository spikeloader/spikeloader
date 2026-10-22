# Security policy

## Supported deployment

Spikeloader is an early-stage, single-operator CI/CD system for trusted repositories
on a local Linux x86_64 host. It is not a hardened multi-tenant service.

- Jobs run as root in disposable Firecracker microVMs inside an outer VM.
- Firecracker currently runs without the jailer. The outer VM is the primary boundary.
- Do not execute untrusted fork pull requests or offer runners to unrelated tenants.
- Keep the panel on loopback. Remote access needs an authenticated/TLS reverse proxy
  and correctly configured Host/Origin allowlists; do not disable authentication.
- The optional local registry is plain HTTP and intended only for the local lab.
- Docker access is effectively host-root access. Protect access to the host and Docker socket.
- Bootstrap downloads upstream images/toolchains. Arch packages are rolling releases;
  setup is repeatable, but builds are not byte-for-byte reproducible.

Protected secrets and log masking reduce accidental exposure; they cannot prevent
trusted pipeline code from deliberately exfiltrating an injected secret.
Protect `.lab/`, database backups, SSH keys, kubeconfigs and sign-in links.
See [docs/backend.md](docs/backend.md) for the implemented security invariants.

## Reporting a vulnerability

Use GitHub's **Security → Report a vulnerability** in
[spikeloader/spikeloader](https://github.com/spikeloader/spikeloader/security).
Maintainers must enable private vulnerability reporting in repository settings.
If it is unavailable, open an issue requesting a private contact channel without
publishing exploit details, tokens, credentials or private source.

Include affected revision, deployment conditions, impact and a minimal sanitized
reproduction in the private report. No response-time or long-term-support guarantee
is currently offered; use the latest revision and review release notes.
