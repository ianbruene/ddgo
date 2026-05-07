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
min_macos="${MACOSX_DEPLOYMENT_TARGET:-14.0}"

case "$arch" in
  arm64|amd64) ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

if [[ ! -f "$bin_path" ]]; then
  echo "input binary not found: $bin_path" >&2
  exit 1
fi

qt_prefix="$(brew --prefix qt@5)"
export PATH="$qt_prefix/bin:$PATH"
export PKG_CONFIG_PATH="$qt_prefix/lib/pkgconfig:${PKG_CONFIG_PATH:-}"
export MACOSX_DEPLOYMENT_TARGET="$min_macos"

out_dir="$(dirname "$out_dmg")"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
out_dmg="$out_dir/$(basename "$out_dmg")"
app_dir="$out_dir/DDGo.app"

bundle_version="${version#v}"
bundle_version="${bundle_version%%[-+]*}"
if [[ ! "$bundle_version" =~ ^[0-9]+([.][0-9]+){0,2}$ ]]; then
  bundle_version="0.0.0"
fi
short_version="$bundle_version"

rm -rf "$app_dir"
mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources"

cat > "$app_dir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>ddgo</string>
<key>CFBundleIdentifier</key><string>com.ianbruene.ddgo</string>
<key>CFBundleName</key><string>DDGo</string>
<key>CFBundleDisplayName</key><string>DDGo</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>${short_version}</string>
<key>CFBundleVersion</key><string>${bundle_version}</string>
<key>LSMinimumSystemVersion</key><string>${min_macos}</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST

cp "$bin_path" "$app_dir/Contents/MacOS/ddgo"
chmod +x "$app_dir/Contents/MacOS/ddgo"

macdeployqt "$app_dir" -verbose=2
codesign --force --deep --sign - "$app_dir"
codesign --verify --deep --strict --verbose=2 "$app_dir"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
mkdir -p "$workdir/dmg-root"
ditto "$app_dir" "$workdir/dmg-root/DDGo.app"
hdiutil create -volname "DDGo" -srcfolder "$workdir/dmg-root" -ov -format UDZO "$out_dmg"

echo "created $out_dmg"
echo "kept app bundle at $app_dir"
