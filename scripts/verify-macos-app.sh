#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 <app-path> <arch> [max-minos]" >&2
  exit 1
fi

version_le() {
  python3 - "$1" "$2" <<'PY'
import sys

def parts(v):
    out = []
    for part in v.split('.'):
        digits = ''.join(ch for ch in part if ch.isdigit())
        out.append(int(digits or '0'))
    while len(out) < 3:
        out.append(0)
    return out[:3]

sys.exit(0 if parts(sys.argv[1]) <= parts(sys.argv[2]) else 1)
PY
}

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
if ! version_le "$plist_min" "$max_minos"; then
  echo "LSMinimumSystemVersion $plist_min exceeds $max_minos" >&2
  exit 1
fi

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
    if ! version_le "$minos" "$max_minos"; then
      echo "minos $minos exceeds $max_minos: $f" >&2
      fail=1
    fi
  done <<< "$minos_lines"
done < <(find "$app/Contents" -type f -print0)

exit "$fail"
