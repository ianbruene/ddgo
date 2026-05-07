#!/usr/bin/env bash
set -euo pipefail
if [[ $# -ne 2 ]]; then echo "usage: $0 <app-path> <arch>" >&2; exit 1; fi
app="$1"; expected="$2"; [[ -d "$app" ]]
fail=0
while IFS= read -r -d '' f; do
  if ! file "$f" | grep -q 'Mach-O'; then continue; fi
  archs="$(lipo -archs "$f")"
  if [[ "$archs" != *"$expected"* ]]; then echo "missing $expected: $f ($archs)"; fail=1; fi
done < <(find "$app" -type f -print0)
exit "$fail"
