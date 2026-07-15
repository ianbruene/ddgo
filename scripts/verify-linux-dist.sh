#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <staged-linux-distribution-dir>" >&2
}

if [[ $# -ne 1 ]]; then
  usage
  exit 2
fi

dist="$1"

require_exec() {
  local path="$1"
  if [[ ! -x "$path" ]]; then
    echo "Required executable missing or not executable: $path" >&2
    exit 1
  fi
}

require_file() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    echo "Required file missing: $path" >&2
    exit 1
  fi
}

require_dir() {
  local path="$1"
  if [[ ! -d "$path" ]]; then
    echo "Required directory missing: $path" >&2
    exit 1
  fi
}

require_exec "$dist/ddgo"
require_exec "$dist/mockgrbl"
require_exec "$dist/run-ddgo.sh"
require_dir "$dist/lib"
require_dir "$dist/plugins"
require_file "$dist/plugins/platforms/libqxcb.so"
require_file "$dist/README-artifact.txt"

if ! find "$dist/lib" -type f -name '*.so*' -print -quit | grep -q .; then
  echo "Bundled lib/ directory is empty" >&2
  exit 1
fi

check_ldd() {
  local label="$1"
  shift
  local output
  echo "== ldd: $label =="
  output="$($@ 2>&1)"
  printf '%s\n' "$output"
  if grep -q 'not found' <<<"$output"; then
    echo "Missing shared library detected by ldd for $label" >&2
    exit 1
  fi
}

check_ldd "ddgo" ldd "$dist/ddgo"
check_ldd "ddgo with bundled runtime environment" env \
  LD_LIBRARY_PATH="$dist/lib" \
  QT_PLUGIN_PATH="$dist/plugins" \
  QT_QPA_PLATFORM_PLUGIN_PATH="$dist/plugins/platforms" \
  ldd "$dist/ddgo"
check_ldd "plugins/platforms/libqxcb.so" env \
  LD_LIBRARY_PATH="$dist/lib" \
  QT_PLUGIN_PATH="$dist/plugins" \
  QT_QPA_PLATFORM_PLUGIN_PATH="$dist/plugins/platforms" \
  ldd "$dist/plugins/platforms/libqxcb.so"

"$dist/mockgrbl" -h

echo "Skipping ddgo CLI smoke test: no known safe help path is guaranteed not to start the GUI/event loop."

echo "== Linux distribution layout =="
find "$dist" -maxdepth 4 -print | sort
