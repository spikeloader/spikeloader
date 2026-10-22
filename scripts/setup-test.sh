#!/usr/bin/env bash
# Exercise first-run setup without Docker, KVM, downloads or access to installation state.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/spikeloader-setup-test.XXXXXXXX")
trap 'rm -rf -- "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/web" "$tmp/tools" "$tmp/host"
cp "$ROOT/bootstrap.sh" "$ROOT/.env.example" "$ROOT/VERSION" "$tmp/"
cp "$ROOT/host/common.sh" "$tmp/host/"
cat >"$tmp/bin/spikeloader" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
test -s "$(dirname "$0")/../.env"
printf '%s\n' "$*" >>"$SETUP_TEST_CALLS"
SH
cat >"$tmp/tools/npm" <<'SH'
#!/usr/bin/env bash
printf 'npm %s\n' "$*" >>"$SETUP_TEST_CALLS"
SH
chmod +x "$tmp/bin/spikeloader" "$tmp/tools/npm"
export SETUP_TEST_CALLS="$tmp/calls"
export XDG_BIN_HOME="$tmp/installed"
export PATH="$tmp/tools:$PATH"

bash "$tmp/bootstrap.sh" --help >"$tmp/help"
test ! -e "$tmp/.env"
if bash -c 'source "$1/host/common.sh"' _ "$tmp" >"$tmp/missing" 2>&1; then
  echo "common.sh accepted a missing configuration" >&2
  exit 1
fi
grep -q 'Missing .*\.env' "$tmp/missing"
bash "$tmp/bootstrap.sh" --no-kernel >"$tmp/output"
cmp "$tmp/.env.example" "$tmp/.env"
test "$(stat -c %a "$tmp/.env")" = 600
test "$(readlink "$tmp/installed/spikeloader")" = "$tmp/bin/spikeloader"
grep -qx doctor "$tmp/calls"
grep -qx build "$tmp/calls"
grep -qx 'db start' "$tmp/calls"
if grep -q '^web url$' "$tmp/calls"; then
  echo "bootstrap printed a sign-in token" >&2
  exit 1
fi
printf '\nCI_MAX_JOBS=1\n' >>"$tmp/.env"
cp "$tmp/.env" "$tmp/expected"
bash "$tmp/bootstrap.sh" --no-kernel >"$tmp/output"
cmp "$tmp/expected" "$tmp/.env"
if bash "$tmp/bootstrap.sh" --invalid >"$tmp/invalid" 2>&1; then
  echo "bootstrap accepted an invalid option" >&2
  exit 1
fi
echo "setup smoke tests passed"
