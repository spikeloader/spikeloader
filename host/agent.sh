#!/usr/bin/env bash
# Builds a static Linux/x86_64 fcagent on the host; stdout is the binary path.
set -euo pipefail
source "$(dirname "$0")/common.sh"

out=$STATE/bin/fcagent
if [[ -x $out && -z $(find "$ROOT/cmd/fcagent" "$ROOT/internal/fcagent" "$ROOT/internal/agent" \
  "$ROOT/internal/env" "$ROOT/go.mod" "$ROOT/go.sum" "$ROOT/host/agent.sh" -newer "$out" -print -quit) ]]; then
  echo "$out"
  exit 0
fi

log "building fcagent (static Linux/x86_64)..." >&2
mkdir -p "$STATE/bin"
go=$(command -v go || true)
[[ -n $go || ! -x $HOME/.local/go/bin/go ]] || go=$HOME/.local/go/bin/go
if [[ -n $go ]]; then
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$go" build -trimpath -o "$out" ./cmd/fcagent)
else
  command -v docker >/dev/null || die "neither Go (https://go.dev/dl) nor Docker is available to build fcagent"
  mkdir -p "$STATE/go"
  docker run --rm --user "$(id -u):$(id -g)" \
    -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod \
    -v "$ROOT:/src" -v "$STATE/go:/cache" -w /src golang:1.26-alpine \
    go build -trimpath -o .lab/bin/fcagent ./cmd/fcagent
fi
echo "$out"
