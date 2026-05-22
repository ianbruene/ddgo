#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <app-path> <expected-arch> <max-minos>" >&2
  exit 1
fi

version_le() {
  python3 - "$1" "$2" <<'PY'
import sys

def parts(v):
    out = []
    for part in str(v).split('.'):
        digits = ''.join(ch for ch in part if (ch.isdigit()))
        out.append(int(digits or '0'))
    while len(out) < 3:
        out.append(0)
    return out[:3]

sys.exit(0 if parts(sys.argv[1]) <= parts(sys.argv[2]) else 1)
PY
}

app="$1"
expected="$2"
max_minos="$3"
plist="$app/Contents/Info.plist"
exe="$app/Contents/MacOS/ddgo"

fail=0
summary=()

mark_fail() {
  local msg="$1"
  echo "ERROR: $msg" >&2
  fail=$((fail + 1))
}

list_loaded_dylibs() {
  local f="$1"
  otool -l "$f" | awk '
    /cmd LC_LOAD_DYLIB|cmd LC_LOAD_WEAK_DYLIB|cmd LC_REEXPORT_DYLIB|cmd LC_LOAD_UPWARD_DYLIB/ {
      in_load = 1
      next
    }
    in_load && /name / {
      print $2
      in_load = 0
      next
    }
    /cmd LC_/ {
      in_load = 0
    }
  '
}

if [[ ! -d "$app" ]]; then
  echo "app bundle not found: $app" >&2
  exit 1
fi
if [[ ! -f "$plist" ]]; then
  mark_fail "missing Info.plist: $plist"
fi
if [[ ! -x "$exe" ]]; then
  mark_fail "missing executable: $exe"
fi

plugin_path=""
if [[ -f "$app/Contents/PlugIns/platforms/libqcocoa.dylib" ]]; then
  plugin_path="$app/Contents/PlugIns/platforms/libqcocoa.dylib"
elif [[ -f "$app/Contents/Plugins/platforms/libqcocoa.dylib" ]]; then
  plugin_path="$app/Contents/Plugins/platforms/libqcocoa.dylib"
fi
if [[ -z "$plugin_path" ]]; then
  mark_fail "missing Cocoa platform plugin: Contents/PlugIns/platforms/libqcocoa.dylib (or Contents/Plugins/...)"
else
  echo "Found Cocoa platform plugin: $plugin_path"
fi

if [[ -f "$plist" ]]; then
  bundle_exe="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist" 2>/dev/null || true)"
  if [[ "$bundle_exe" != "ddgo" ]]; then
    mark_fail "CFBundleExecutable is '$bundle_exe' (expected 'ddgo')"
  fi
  plist_min="$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "$plist" 2>/dev/null || true)"
  if [[ -z "$plist_min" ]]; then
    mark_fail "LSMinimumSystemVersion missing in Info.plist"
  elif ! version_le "$plist_min" "$max_minos"; then
    mark_fail "LSMinimumSystemVersion $plist_min exceeds $max_minos"
  fi
fi

echo "path | archs | deployment-targets | status"

while IFS= read -r -d '' f; do
  if ! file "$f" | grep -q 'Mach-O'; then
    continue
  fi

  status="ok"
  archs="$(lipo -archs "$f" 2>/dev/null || echo unknown)"
  dep_targets=()
  has_dep_cmd=0
  important=0

  if [[ " $archs " != *" $expected "* ]]; then
    status="fail"
    mark_fail "missing arch $expected: $f ($archs)"
  fi

  build_minos="$(otool -l "$f" | awk '/LC_BUILD_VERSION/{inb=1; next} inb && /minos/{print $2; inb=0}')"
  version_min="$(otool -l "$f" | awk '/LC_VERSION_MIN_MACOSX/{inv=1; next} inv && /version/{print $2; inv=0}')"

  while IFS= read -r d; do
    [[ -z "$d" ]] && continue
    has_dep_cmd=1
    dep_targets+=("minos:$d")
    if ! version_le "$d" "$max_minos"; then
      status="fail"
      mark_fail "LC_BUILD_VERSION minos $d exceeds $max_minos: $f"
    fi
  done <<< "$build_minos"

  while IFS= read -r d; do
    [[ -z "$d" ]] && continue
    has_dep_cmd=1
    dep_targets+=("version:$d")
    if ! version_le "$d" "$max_minos"; then
      status="fail"
      mark_fail "LC_VERSION_MIN_MACOSX version $d exceeds $max_minos: $f"
    fi
  done <<< "$version_min"

  if [[ "$f" == "$exe" ]] || [[ "$f" == *"Qt"*.framework/* ]] || [[ "$f" == *"/PlugIns/"* ]] || [[ "$f" == *"/Plugins/"* ]]; then
    important=1
  fi

  if [[ $has_dep_cmd -eq 0 ]]; then
    echo "WARN: no macOS deployment load command found: $f"
    dep_targets+=("none")
    if [[ $important -eq 1 ]]; then
      status="fail"
      mark_fail "missing macOS deployment load command for required Mach-O: $f"
    fi
  fi

  while IFS= read -r dep_path; do
    [[ -z "$dep_path" ]] && continue
    case "$dep_path" in
      @executable_path/*|@loader_path/*|@rpath/*|/System/Library/*|/usr/lib/*) ;;
      /usr/local/opt/qt@5/*|/opt/homebrew/opt/qt@5/*|/usr/local/Cellar/qt@5/*|/opt/homebrew/Cellar/qt@5/*)
        status="fail"
        mark_fail "unbundled Homebrew Qt loaded dependency path in $f: $dep_path"
        ;;
      *)
        status="fail"
        mark_fail "disallowed loaded dependency path in $f: $dep_path"
        ;;
    esac
  done < <(list_loaded_dylibs "$f")

  dep_str="${dep_targets[*]:-none}"
  dep_str="${dep_str// /,}"
  echo "$f | $archs | $dep_str | $status"
done < <(find "$app/Contents" -type f -print0)

if [[ $fail -eq 0 ]]; then
  echo "macOS app verification passed"
else
  echo "macOS app verification failed with $fail issue(s)" >&2
  exit 1
fi
