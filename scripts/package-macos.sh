#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 3 ]]; then
  echo "usage: $0 <arm64|amd64> <input-binary> <output-dmg> [version]" >&2
  exit 1
fi

arch="$1"
bin_path="$2"
out_dmg="$3"
version="${4:-0.0.0}"

out_dir="$(dirname "$out_dmg")"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
out_dmg="$out_dir/$(basename "$out_dmg")"
app_dir="$out_dir/DDGo.app"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "$script_dir/package-macos-app.sh" "$arch" "$bin_path" "$app_dir" "$version"
bash "$script_dir/create-macos-dmg.sh" "$app_dir" "$out_dmg"

echo "created $out_dmg"
echo "kept app bundle at $app_dir"
