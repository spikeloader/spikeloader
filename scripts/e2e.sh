#!/usr/bin/env bash
# Uses an already running daemon; the engine alone owns VM startup and shutdown.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLI="$ROOT/bin/spikeloader"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/spikeloader-e2e.XXXXXXXX")
PROJECT="e2e-$(date +%s)-$$"
CREATED=0
cleanup() {
  local rc=$?
  trap - EXIT
  if (( CREATED )); then
    if ! "$CLI" ci rm "$PROJECT"; then
      echo "cleanup failed: remove project $PROJECT manually" >&2
      rc=1
    fi
  fi
  rm -rf -- "$WORK"
  exit "$rc"
}
trap cleanup EXIT
git init -q -b main "$WORK/repo"
git -C "$WORK/repo" config user.name "Spikeloader E2E"
git -C "$WORK/repo" config user.email "e2e@localhost"
cat >"$WORK/repo/.spikeloader.yml" <<'YAML'
stages: [build, test]
jobs:
  build:
    stage: build
    secrets: [DEMO_TOKEN]
    cache:
      key: e2e-cache
      paths: [.cache/]
    artifacts:
      paths: [out/]
    steps:
      - 'printf "token=%s\n" "$DEMO_TOKEN"'
      - 'if test -f .cache/marker; then echo CACHE_RESTORED; fi'
      - 'mkdir -p .cache out; echo retained >.cache/marker; echo artifact-ok >out/result.txt'
  check:
    stage: test
    matrix:
      SHARD: [1, 2]
    steps:
      - 'test "$(cat out/result.txt)" = artifact-ok'
      - 'echo "ARTIFACT_OK shard=$MATRIX_SHARD"'
YAML
git -C "$WORK/repo" add .
git -C "$WORK/repo" commit -qm "Add disposable e2e pipeline

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
git -C "$WORK/repo" checkout -qb feature
git -C "$WORK/repo" commit --allow-empty -qm "Advance unprotected branch

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
git -C "$WORK/repo" checkout -q main
git -C "$WORK/repo" tag release-v1
"$CLI" ci add "$PROJECT" "file://$WORK/repo" --poll 0
CREATED=1
printf 'synthetic-e2e-secret' | "$CLI" ci secret set "$PROJECT" DEMO_TOKEN
"$CLI" ci secret ls "$PROJECT" | grep -F 'DEMO_TOKEN [protected]'
"$CLI" ci lint "$WORK/repo/.spikeloader.yml"
"$CLI" ci run "$PROJECT" main -f >"$WORK/main.log"
grep -F 'token=***' "$WORK/main.log"
if grep -q 'synthetic-e2e-secret' "$WORK/main.log"; then echo "secret leaked" >&2; exit 1; fi
grep -F 'ARTIFACT_OK shard=1' "$WORK/main.log"
grep -F 'ARTIFACT_OK shard=2' "$WORK/main.log"
"$CLI" ci run "$PROJECT" main -f >"$WORK/cache.log"
grep -F 'CACHE_RESTORED' "$WORK/cache.log"
if "$CLI" ci run "$PROJECT" feature -f >"$WORK/denied.log" 2>&1; then
  echo "unprotected branch unexpectedly received protected secret" >&2; exit 1
fi
grep -F 'secret DEMO_TOKEN is only available on protected branches' "$WORK/denied.log"
SHA=$(git -C "$WORK/repo" rev-parse HEAD)
if "$CLI" ci run "$PROJECT" "$SHA" -f >"$WORK/detached.log" 2>&1; then
  echo "detached commit unexpectedly received protected secret" >&2; exit 1
fi
grep -F 'secret DEMO_TOKEN is only available on protected branches' "$WORK/detached.log"
if "$CLI" ci run "$PROJECT" release-v1 -f >"$WORK/tag.log" 2>&1; then
  echo "tag unexpectedly received protected secret" >&2; exit 1
fi
grep -F 'secret DEMO_TOKEN is only available on protected branches' "$WORK/tag.log"
printf 'synthetic-e2e-secret' | "$CLI" ci secret set "$PROJECT" DEMO_TOKEN --unprotected
"$CLI" ci run "$PROJECT" feature -f >"$WORK/feature.log"
grep -F 'token=***' "$WORK/feature.log"
RUN=$(sed -n 's/^run #\([0-9]*\) started.*/\1/p' "$WORK/feature.log" | head -n 1)
"$CLI" ci runs "$PROJECT"
"$CLI" ci show "$RUN"
"$CLI" ci logs "$RUN" >"$WORK/download.log"
grep -F 'ARTIFACT_OK shard=2' "$WORK/download.log"
"$CLI" ci secret rm "$PROJECT" DEMO_TOKEN
"$CLI" ci secret ls "$PROJECT" | grep -F 'no secrets'
echo "e2e passed: Go CLI, artifacts/matrix/cache, secret masking and protected branch policy"
