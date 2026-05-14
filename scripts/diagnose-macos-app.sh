#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <path-to-DDGo.app>" >&2
  exit 1
fi

app="$1"
if [[ ! -d "$app/Contents" ]]; then
  echo "invalid app bundle: $app" >&2
  exit 1
fi

sw_vers || true
uname -m || true

if [[ -f "$app/Contents/MacOS/ddgo" ]]; then
  file "$app/Contents/MacOS/ddgo" || true
  lipo -archs "$app/Contents/MacOS/ddgo" || true
fi

if [[ -f "$app/Contents/Info.plist" ]]; then
  plutil -p "$app/Contents/Info.plist" || true
fi

find "$app/Contents" -type f -print0 | while IFS= read -r -d '' f; do
  if file "$f" | grep -q 'Mach-O'; then
    echo "== $f"
    file "$f" || true
    lipo -archs "$f" || true
    otool -L "$f" || true
    otool -l "$f" | grep -A4 LC_BUILD_VERSION || true
    otool -l "$f" | grep -A3 LC_VERSION_MIN_MACOSX || true
  fi
done
