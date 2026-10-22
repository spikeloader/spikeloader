# shellcheck shell=bash
# Helpers shared by the host scripts.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE="$ROOT/.lab"
IMAGE=fclab-qemu
CONTAINER=fclab-outer
if [[ ! -f $ROOT/.env ]]; then
  printf 'Missing %s/.env; run ./bootstrap.sh or copy .env.example to .env first.\n' "$ROOT" >&2
  exit 1
fi
# shellcheck source=../.env
source "$ROOT/.env"

mkdir -p "$STATE"

log() { printf '\033[1;34m[spikeloader]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[spikeloader]\033[0m %s\n' "$*" >&2; exit 1; }

resolve_backend() {
  if [[ $BACKEND == auto ]]; then
    if command -v qemu-system-x86_64 >/dev/null && command -v qemu-img >/dev/null \
       && command -v cloud-localds >/dev/null; then
      BACKEND=native
    elif command -v docker >/dev/null; then
      BACKEND=docker
    else
      die "Install qemu-system-x86 qemu-utils cloud-image-utils, or Docker."
    fi
  fi
}

# Runs a tool (qemu-img, cloud-localds...) natively or in the container.
# The state directory is mounted at the same path, so paths are identical.
tool() {
  if [[ $BACKEND == native ]]; then
    "$@"
  else
    docker run --rm --user "$(id -u):$(id -g)" -v "$STATE:$STATE" -w "$STATE" "$IMAGE" "$@"
  fi
}

# the outer VM's host key is pinned on first use (reset when its disk is recreated): see internal/sshx
KEY_OPTS=(-i "$STATE/id_ed25519" -o StrictHostKeyChecking=accept-new
          -o UserKnownHostsFile="$STATE/known_hosts" -o HostKeyAlias=spikeloader-outer
          -o LogLevel=ERROR -o ConnectTimeout=5)
SSH_OPTS=("${KEY_OPTS[@]}" -p "$SSH_PORT")

outer_ssh() { ssh "${SSH_OPTS[@]}" lab@127.0.0.1 "$@"; }

outer_running() {
  if [[ -f $STATE/backend ]] && [[ $(cat "$STATE/backend") == docker ]]; then
    [[ $(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null) == true ]]
  else
    [[ -f $STATE/qemu.pid ]] && kill -0 "$(cat "$STATE/qemu.pid")" 2>/dev/null
  fi
}
