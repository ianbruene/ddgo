#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <ddgo-binary> <mockgrbl-binary> <output-dir>" >&2
}

if [[ $# -ne 3 ]]; then
  usage
  exit 2
fi

ddgo_binary="$1"
mockgrbl_binary="$2"
out_dir="$3"

if [[ ! -x "$ddgo_binary" ]]; then
  echo "ddgo binary is missing or not executable: $ddgo_binary" >&2
  exit 1
fi

if [[ ! -x "$mockgrbl_binary" ]]; then
  echo "mockgrbl binary is missing or not executable: $mockgrbl_binary" >&2
  exit 1
fi

if command -v qmake >/dev/null 2>&1; then
  qmake_cmd=qmake
elif command -v qmake-qt5 >/dev/null 2>&1; then
  qmake_cmd=qmake-qt5
else
  echo "qmake/qmake-qt5 is required to locate Qt plugins" >&2
  exit 1
fi

qt_plugins_dir="$($qmake_cmd -query QT_INSTALL_PLUGINS)"
if [[ -z "$qt_plugins_dir" || ! -d "$qt_plugins_dir" ]]; then
  echo "Qt plugins directory not found: $qt_plugins_dir" >&2
  exit 1
fi

rm -rf "$out_dir"
mkdir -p "$out_dir/lib" "$out_dir/plugins"

cp "$ddgo_binary" "$out_dir/ddgo"
cp "$mockgrbl_binary" "$out_dir/mockgrbl"
chmod 0755 "$out_dir/ddgo" "$out_dir/mockgrbl"

is_excluded_lib() {
  local lib="$1"
  local base
  base="$(basename "$lib")"

  case "$base" in
    linux-vdso.so*|ld-linux*.so*|libc.so.*|libpthread.so.*|libdl.so.*|libm.so.*|librt.so.*)
      return 0
      ;;
  esac

  case "$lib" in
    /lib64/ld-linux*|/lib/x86_64-linux-gnu/libc.so.*|/lib/x86_64-linux-gnu/libpthread.so.*|/lib/x86_64-linux-gnu/libdl.so.*|/lib/x86_64-linux-gnu/libm.so.*|/lib/x86_64-linux-gnu/librt.so.*)
      return 0
      ;;
  esac

  return 1
}

copy_one_lib() {
  local lib="$1"
  [[ -n "$lib" && -f "$lib" ]] || return 0
  if is_excluded_lib "$lib"; then
    return 0
  fi
  cp -L -n "$lib" "$out_dir/lib/"
}

copy_ldd_deps() {
  local file="$1"
  ldd "$file" | awk '/=>/ { print $(NF-1) } /^\// { print $1 }' | while read -r lib; do
    copy_one_lib "$lib"
  done
}

copy_plugin_path() {
  local rel="$1"
  local src="$qt_plugins_dir/$rel"
  local dest="$out_dir/plugins/$rel"

  if [[ ! -e "$src" ]]; then
    echo "Optional Qt plugin path not found, skipping: $rel"
    return 0
  fi

  mkdir -p "$(dirname "$dest")"
  cp -aL "$src" "$dest"
}

copy_ldd_deps "$out_dir/ddgo"

copy_plugin_path "platforms/libqxcb.so"
for plugin_dir in xcbglintegrations imageformats styles platformthemes iconengines; do
  copy_plugin_path "$plugin_dir"
done

if [[ ! -f "$out_dir/plugins/platforms/libqxcb.so" ]]; then
  echo "Required Qt platform plugin missing after packaging: plugins/platforms/libqxcb.so" >&2
  exit 1
fi

while IFS= read -r plugin_so; do
  copy_ldd_deps "$plugin_so"
done < <(find "$out_dir/plugins" -type f -name '*.so*' -print | sort)

cat > "$out_dir/run-ddgo.sh" <<'LAUNCHER'
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export LD_LIBRARY_PATH="$ROOT/lib:${LD_LIBRARY_PATH:-}"
export QT_PLUGIN_PATH="$ROOT/plugins:${QT_PLUGIN_PATH:-}"
export QT_QPA_PLATFORM_PLUGIN_PATH="$ROOT/plugins/platforms"
exec "$ROOT/ddgo" "$@"
LAUNCHER
chmod 0755 "$out_dir/run-ddgo.sh"

cat > "$out_dir/README-artifact.txt" <<'README'
DDGo Linux amd64 artifact.

Contents:
- ddgo: DDGo CNC UI command built for Linux amd64 with MIQT UI and serial support.
- mockgrbl: GrblDD mock machine for local development/testing.
- run-ddgo.sh: launcher that configures bundled library and Qt plugin paths before starting ddgo.
- lib/: shared libraries bundled from the build runner for the DDGo GUI runtime.
- plugins/: Qt plugins used by the MIQT/Qt GUI, including the xcb platform plugin.

Run DDGo with:
  ./run-ddgo.sh

The launcher sets LD_LIBRARY_PATH, QT_PLUGIN_PATH, and QT_QPA_PLATFORM_PLUGIN_PATH so the app can find the bundled runtime libraries and Qt plugins. Running ./ddgo directly may fail on systems that do not already provide matching Qt libraries/plugins.

mockgrbl can be run directly from this folder. It is Linux PTY-backed and accepts:
  -symlink <path>
  -http <addr>

This artifact is built on ubuntu-latest and bundles Qt/runtime libraries needed by the app. Compatibility with older distributions is limited by the runner's glibc/system ABI.
README

if ! find "$out_dir/lib" -type f -name '*.so*' -print -quit | grep -q .; then
  echo "No shared libraries were copied into $out_dir/lib" >&2
  exit 1
fi

find "$out_dir" -type f \( -name '*.sh' -o -name 'ddgo' -o -name 'mockgrbl' \) -exec chmod 0755 {} +

echo "Packaged Linux app at $out_dir"
find "$out_dir" -maxdepth 4 -print | sort
