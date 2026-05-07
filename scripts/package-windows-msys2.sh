#!/usr/bin/env bash
set -euo pipefail
if [[ $# -ne 2 ]]; then echo "usage: $0 <input-exe> <output-zip>" >&2; exit 1; fi
exe="$1"; out_zip="$2"
workdir="$(mktemp -d)"; trap 'rm -rf "$workdir"' EXIT
mkdir -p "$workdir/dist/platforms"; cp "$exe" "$workdir/dist/DDGo.exe"
qt_bin="${MINGW_PREFIX:-/mingw64}/bin"
for dll in Qt5Core.dll Qt5Gui.dll Qt5Widgets.dll libgcc_s_seh-1.dll libstdc++-6.dll libwinpthread-1.dll; do cp "$qt_bin/$dll" "$workdir/dist/$dll"; done
cp "$qt_bin/platforms/qwindows.dll" "$workdir/dist/platforms/qwindows.dll"
( cd "$workdir/dist" && zip -r "$out_zip" . )
echo "created $out_zip"
