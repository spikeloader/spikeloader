#!/usr/bin/env bash
# Shuts down the outer VM. With --purge it also deletes the disk (keeps the downloaded base image).
set -euo pipefail
source "$(dirname "$0")/common.sh"

# Clean shutdown: stop the microVMs, flush caches to disk and power off.
# Only then force QEMU to exit (avoids losing recent writes).
if outer_running && outer_ssh true 2>/dev/null; then
  log "shutting down the outer VM (clean shutdown)..."
  outer_ssh 'sudo fcctl stop-all; sudo sync; sudo systemctl poweroff' || true
  for _ in $(seq 60); do outer_running || break; sleep 1; done
fi

if [[ -f $STATE/backend ]] && [[ $(cat "$STATE/backend") == docker ]]; then
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
elif [[ -f $STATE/qemu.pid ]]; then
  kill "$(cat "$STATE/qemu.pid")" 2>/dev/null || true
fi
rm -f "$STATE/qemu.pid" "$STATE/backend"
log "outer VM is off."

if [[ ${1:-} == --purge ]]; then
  rm -f "$STATE/disk.qcow2" "$STATE/seed.iso" "$STATE/user-data" "$STATE/console.log" "$STATE/known_hosts"
  log "outer VM disk removed."
fi
