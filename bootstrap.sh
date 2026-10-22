#!/usr/bin/env bash
# Self-hosted setup of spikeloader for the current user (no sudo). Idempotent: run it again to update.
#
#   ./bootstrap.sh              prerequisites, panel, `spikeloader` command, database, microVM kernel
#   ./bootstrap.sh --k3s        + local Kubernetes (k3s) and image registry to test deploys
#   ./bootstrap.sh --service    + panel as a systemd user service (starts at login)
#   ./bootstrap.sh --no-kernel  skip the microVM kernel build (the first CI job builds it instead)
#
# Flags combine: ./bootstrap.sh --k3s --service
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"
SL="$ROOT/bin/spikeloader"

k3s=0 service=0 kernel=1
for arg in "$@"; do
  case $arg in
    --k3s) k3s=1 ;;
    --service) service=1 ;;
    --no-kernel) kernel=0 ;;
    -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $arg (see ./bootstrap.sh --help)" >&2; exit 1 ;;
  esac
done

[[ $EUID -ne 0 ]] || { echo "run as your own user, without sudo" >&2; exit 1; }
step() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*"; }

if [[ ! -e $ROOT/.env ]]; then
  step "creating local configuration from .env.example"
  (umask 077; cp "$ROOT/.env.example" "$ROOT/.env")
  echo "Review $ROOT/.env for resource sizes and ports; existing configuration is never overwritten."
fi

step "checking prerequisites"
if ! "$SL" doctor; then
  cat >&2 <<'EOF'

Fix the items marked ✗ and run ./bootstrap.sh again. Common fixes:
  KVM / Docker groups   sudo usermod -aG kvm,docker "$USER"   (then log out and back in)
  nested virtualization echo "options kvm_intel nested=1" | sudo tee /etc/modprobe.d/kvm-nested.conf
                        (kvm_amd on AMD), then reboot or reload the kvm module
EOF
  exit 1
fi

step "daemon (Go) and web UI (Next.js)"
(cd "$ROOT/web" && npm ci --no-audit --no-fund --loglevel=error)
"$SL" build

step "spikeloader command"
mkdir -p "$BIN_DIR"
ln -sfn "$SL" "$BIN_DIR/spikeloader"
path_hint=""
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) path_hint="add to your shell profile: export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
"$SL" web token >/dev/null

step "database (PostgreSQL container)"
"$SL" db start

if ((kernel)); then
  step "microVM kernel (built once, a few minutes the first time)"
  "$ROOT/host/kernel.sh" >/dev/null
fi

if ((k3s)); then
  step "local Kubernetes (k3s) and registry"
  "$ROOT/host/k3s.sh"
fi

if ((service)); then
  step "systemd user service"
  "$SL" web service install
fi

step "done — spikeloader $(cat "$ROOT/VERSION")"
[[ -z $path_hint ]] || echo "  $path_hint"
if ((service)); then
  echo "  panel:        http://127.0.0.1:4100"
else
  echo "  start:        make up        (or: spikeloader web start)"
fi
echo "  sign-in link: $SL web url (contains your token; do not share it)"
cat <<EOF
  first project: spikeloader ci add my-app <git-url> && spikeloader ci run my-app -f
                 (the first job boots and provisions the outer VM: ~3 min once, ~20 s afterwards)
EOF
if ((k3s)); then
  gw=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
  cat <<EOF
  deploy target: KUBECONFIG=~/.kube/spikeloader-k3s.yaml ci/k8s/create-deployer.sh my-app \\
                   --server https://$gw:6443 | spikeloader ci secret set my-app KUBECONFIG_DATA
                 images: push to $gw:5000/<name>:<tag> (plain HTTP)
EOF
fi
