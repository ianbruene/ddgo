#!/usr/bin/env bash
set -euo pipefail

TMP="${RUNNER_TEMP:-/tmp}"
BIN="$TMP/mockgrbl"
SYMLINK="$TMP/ddgo-mock-grbl-smoke-$$"
HTTP_ADDR="127.0.0.1:18088"
LOG="$TMP/mockgrbl-smoke.log"

rm -f "$BIN" "$SYMLINK" "$LOG"

go build -o "$BIN" ./cmd/mockgrbl

"$BIN" -symlink "$SYMLINK" -http "$HTTP_ADDR" >"$LOG" 2>&1 &
PID=$!

cleanup() {
  kill "$PID" 2>/dev/null || true
  wait "$PID" 2>/dev/null || true
  rm -f "$SYMLINK"
}
trap cleanup EXIT

for _ in {1..100}; do
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "mockgrbl exited before becoming ready" >&2
    break
  fi
  if [ -L "$SYMLINK" ] && curl -fsS "http://$HTTP_ADDR/state" >/dev/null; then
    echo "mockgrbl smoke test ready"
    exit 0
  fi
  sleep 0.1
done

echo "mockgrbl did not become ready" >&2
echo "--- mockgrbl log ---" >&2
cat "$LOG" >&2 || true
echo "--- symlink ---" >&2
ls -l "$SYMLINK" >&2 || true
exit 1
