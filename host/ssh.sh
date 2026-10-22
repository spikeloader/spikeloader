#!/usr/bin/env bash
# SSH into the outer VM. Extra arguments become the remote command.
set -euo pipefail
source "$(dirname "$0")/common.sh"
exec ssh "${SSH_OPTS[@]}" -t lab@127.0.0.1 "$@"
