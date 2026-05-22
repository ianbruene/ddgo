#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <input-exe> <output-zip>" >&2
  exit 1
fi

exe="$1"
out_zip="$2"

out_dir="$(dirname "$out_zip")"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
out_zip="$out_dir/$(basename "$out_zip")"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

stage="$workdir/DDGo"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "$script_dir/stage-windows-msys2.sh" "$exe" "$stage"

(
  cd "$workdir"
  rm -f "$out_zip"
  zip -r "$out_zip" DDGo
)

echo "created $out_zip"
