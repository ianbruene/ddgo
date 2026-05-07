#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <app-path> <arch> [max-minos]" >&2
  exit 1
fi

app="$1"
expected="$2"
max_minos="${3:-14.0}"
exe="$app/Contents/MacOS/ddgo"
plist="$app/Contents/Info.plist"

if [[ ! -d "$app" ]]; then
  echo "app bundle not found: $app" >&2
  exit 1
fi
if [[ ! -x "$exe" ]]; then
  echo "app executable missing or not executable: $exe" >&2
  exit 1
fi
if [[ ! -f "$plist" ]]; then
  echo "Info.plist missing: $plist" >&2
  exit 1
fi

plist_min="$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "$plist")"
python3 - "$plist_min" "$max_minos" <<'PY'
import sys
from packaging.version import Version
actual = Version(sys.argv[1])
limit = Version(sys.argv[2])
if actual > limit:
    raise SystemExit(f"LSMinimumSystemVersion {actual} exceeds {limit}")
PY

fail=0
while IFS= read -r -d '' f; do
  if ! file "$f" | grep -q 'Mach-O'; then
    continue
  fi
  archs="$(lipo -archs "$f")"
  if [[ " $archs " != *" $expected "* ]]; then
    echo "missing $expected: $f ($archs)" >&2
    fail=1
  fi
  minos_lines="$(otool -l "$f" | awk '/LC_BUILD_VERSION/{in_build=1; next} in_build && /minos/{print $2; in_build=0}')"
  while IFS= read -r minos; do
    [[ -z "$minos" ]] && continue
    if ! python3 - "$minos" "$max_minos" <<'PY'
import sys
from packaging.version import Version
sys.exit(0 if Version(sys.argv[1]) <= Version(sys.argv[2]) else 1)
PY
    then
      echo "minos $minos exceeds $max_minos: $f" >&2
      fail=1
    fi
  done <<< "$minos_lines"
done < <(find "$app/Contents" -type f -print0)

exit "$fail"
