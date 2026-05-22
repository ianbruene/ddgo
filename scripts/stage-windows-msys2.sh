#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <input-exe> <output-dir>" >&2
  exit 1
fi

exe="$1"
stage="$2"
prefix="${MINGW_PREFIX:-/mingw64}"
qt_bin="$prefix/bin"
qt_plugins="$prefix/share/qt5/plugins"

if [[ ! -f "$exe" ]]; then
  echo "input exe not found: $exe" >&2
  exit 1
fi
if [[ ! -d "$qt_bin" ]]; then
  echo "Qt/MinGW bin directory not found: $qt_bin" >&2
  exit 1
fi

rm -rf "$stage"
mkdir -p "$stage/platforms"
cp "$exe" "$stage/DDGo.exe"

if command -v windeployqt >/dev/null 2>&1; then
  windeployqt --release "$stage/DDGo.exe"
fi

required_dlls=(
  Qt5Core.dll
  Qt5Gui.dll
  Qt5Widgets.dll
  libgcc_s_seh-1.dll
  libstdc++-6.dll
  libwinpthread-1.dll
)
for dll in "${required_dlls[@]}"; do
  if [[ -f "$qt_bin/$dll" ]]; then
    cp -n "$qt_bin/$dll" "$stage/$dll"
  else
    echo "required DLL missing from MSYS2 prefix: $qt_bin/$dll" >&2
    exit 1
  fi
done

if [[ -f "$qt_plugins/platforms/qwindows.dll" ]]; then
  cp -n "$qt_plugins/platforms/qwindows.dll" "$stage/platforms/qwindows.dll"
elif [[ -f "$qt_bin/platforms/qwindows.dll" ]]; then
  cp -n "$qt_bin/platforms/qwindows.dll" "$stage/platforms/qwindows.dll"
else
  echo "qwindows.dll not found under $qt_plugins/platforms or $qt_bin/platforms" >&2
  exit 1
fi

while IFS= read -r dll; do
  [[ -z "$dll" || ! -f "$dll" ]] && continue
  cp -n "$dll" "$stage/"
done < <(
  find "$stage" -maxdepth 2 -type f \( -name '*.exe' -o -name '*.dll' \) -print0 |
    xargs -0 -r ldd 2>/dev/null |
    awk '/\/mingw64\/bin\// || /\/ucrt64\/bin\// {print $3}' |
    sort -u
)

echo "staged Windows app at $stage"
find "$stage" -type f | sort
