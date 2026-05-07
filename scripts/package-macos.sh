#!/usr/bin/env bash
set -euo pipefail
if [[ $# -lt 3 ]]; then echo "usage: $0 <arm64|amd64> <input-binary> <output-dmg> [version]" >&2; exit 1; fi
arch="$1"; bin_path="$2"; out_dmg="$3"; version="${4:-0.0.0}"
case "$arch" in arm64) min_macos="11.0";; amd64) min_macos="10.15";; *) echo "unsupported arch: $arch" >&2; exit 1;; esac
qt_prefix="$(brew --prefix qt@5)"; export PATH="$qt_prefix/bin:$PATH"
workdir="$(mktemp -d)"; trap 'rm -rf "$workdir"' EXIT
app_dir="$workdir/DDGo.app"; mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources"
cat > "$app_dir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>ddgo</string>
<key>CFBundleIdentifier</key><string>com.ianbruene.ddgo</string>
<key>CFBundleName</key><string>DDGo</string>
<key>CFBundleDisplayName</key><string>DDGo</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>${version}</string>
<key>CFBundleVersion</key><string>${version}</string>
<key>LSMinimumSystemVersion</key><string>${min_macos}</string>
</dict></plist>
PLIST
cp "$bin_path" "$app_dir/Contents/MacOS/ddgo"; chmod +x "$app_dir/Contents/MacOS/ddgo"
macdeployqt "$app_dir" -verbose=2
codesign --force --deep --sign - "$app_dir"; codesign --verify --deep --strict --verbose=2 "$app_dir"
mkdir -p "$workdir/dmg-root"; cp -R "$app_dir" "$workdir/dmg-root/DDGo.app"
hdiutil create -volname "DDGo" -srcfolder "$workdir/dmg-root" -ov -format UDZO "$out_dmg"
echo "created $out_dmg"
