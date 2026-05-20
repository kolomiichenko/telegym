#!/usr/bin/env bash
# Runs a k6 scenario against a freshly-launched mock + echobot.
# Usage (from this dir):  ./run.sh <scenario>            # default: echo
# Scenarios live in scenarios/, binaries are taken from <repo-root>/bin/.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
cd "$HERE"

SCENARIO="${1:-echo}"
SCRIPT="scenarios/${SCENARIO}.js"
[[ -f "$SCRIPT" ]] || { echo "no such scenario: $SCRIPT" >&2; exit 1; }

K6="$ROOT/bin/k6"
MOCK="$ROOT/bin/telegym-mock"
BOT="$ROOT/bin/echobot"
[[ -x "$K6" && -x "$MOCK" && -x "$BOT" ]] || {
  echo "build first: (cd $ROOT && make build build-examples)" >&2; exit 1;
}

cleanup() {
  [[ -n "${MOCK_PID:-}" ]] && kill "$MOCK_PID" 2>/dev/null || true
  [[ -n "${BOT_PID:-}"  ]] && kill "$BOT_PID"  2>/dev/null || true
}
trap cleanup EXIT

# Free ports defensively (previous runs sometimes leak).
lsof -ti :5678 :8443 2>/dev/null | xargs -r kill 2>/dev/null || true
sleep 0.3

"$MOCK" -quiet >/tmp/telegym-mock.log 2>&1 &
MOCK_PID=$!
sleep 0.5

"$BOT" >/tmp/echobot.log 2>&1 &
BOT_PID=$!
sleep 0.8

TELEGYM_MOCK_URL=http://localhost:5678 \
TELEGYM_BOT_TOKEN=1234567890:telegym_default_mock_token_xxxxxxxx \
  "$K6" run "$SCRIPT"
