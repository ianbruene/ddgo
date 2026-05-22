#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <app-path> <output-dmg>" >&2
  exit 1
fi

app="$1"
out_dmg="$2"

if [[ ! -d "$app" ]]; then
  echo "app bundle not found: $app" >&2
  exit 1
fi

mkdir -p "$(dirname "$out_dmg")"
rm -f "$out_dmg"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
mkdir -p "$workdir/dmg-root"
ditto "$app" "$workdir/dmg-root/DDGo.app"

if ! hdiutil create -volname "DDGo" -srcfolder "$workdir/dmg-root" -ov -format UDZO "$out_dmg"; then
  echo "hdiutil create failed; mounted images:" >&2
  hdiutil info >&2 || true
  sleep 5
  rm -f "$out_dmg"
  hdiutil create -volname "DDGo" -srcfolder "$workdir/dmg-root" -ov -format UDZO "$out_dmg"
fi
