#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <arm64-app> <amd64-app> <out-app>" >&2
  exit 1
fi

arm_app="$1"
amd_app="$2"
out_app="$3"

for app in "$arm_app" "$amd_app"; do
  if [[ ! -d "$app" ]]; then
    echo "app bundle not found: $app" >&2
    exit 1
  fi
  if [[ ! -f "$app/Contents/Info.plist" ]]; then
    echo "missing Info.plist: $app/Contents/Info.plist" >&2
    exit 1
  fi
  if [[ ! -f "$app/Contents/MacOS/ddgo" ]]; then
    echo "missing executable: $app/Contents/MacOS/ddgo" >&2
    exit 1
  fi
done

rm -rf "$out_app"
mkdir -p "$(dirname "$out_app")"
ditto "$arm_app" "$out_app"

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

(
  cd "$arm_app"
  find . -type f -print | sort
) > "$tmpdir/arm-files.txt"

(
  cd "$amd_app"
  find . -type f -print | sort
) > "$tmpdir/amd-files.txt"

cat "$tmpdir/arm-files.txt" "$tmpdir/amd-files.txt" | sort -u > "$tmpdir/all-files.txt"

merged=0
skipped=0
warnings=0

while IFS= read -r rel; do
  [[ -z "$rel" ]] && continue
  arm_file="$arm_app/$rel"
  amd_file="$amd_app/$rel"
  out_file="$out_app/$rel"

  arm_exists=0
  amd_exists=0
  [[ -f "$arm_file" ]] && arm_exists=1
  [[ -f "$amd_file" ]] && amd_exists=1

  arm_is_macho=0
  amd_is_macho=0
  if [[ $arm_exists -eq 1 ]] && file "$arm_file" | grep -q 'Mach-O'; then arm_is_macho=1; fi
  if [[ $amd_exists -eq 1 ]] && file "$amd_file" | grep -q 'Mach-O'; then amd_is_macho=1; fi

  if [[ $arm_is_macho -eq 1 || $amd_is_macho -eq 1 ]]; then
    if [[ $arm_exists -ne 1 || $amd_exists -ne 1 ]]; then
      echo "Mach-O file exists only in one slice; cannot create universal app:" >&2
      echo "$rel" >&2
      exit 1
    fi

    arm_archs="$(lipo -archs "$arm_file")"
    amd_archs="$(lipo -archs "$amd_file")"

    if [[ " $arm_archs " != *" arm64 "* ]]; then
      echo "arm64 slice missing arm64 architecture: $rel ($arm_archs)" >&2
      exit 1
    fi
    if [[ " $amd_archs " != *" x86_64 "* ]]; then
      echo "amd64 slice missing x86_64 architecture: $rel ($amd_archs)" >&2
      exit 1
    fi

    lipo -create -arch arm64 "$arm_file" -arch x86_64 "$amd_file" -output "$out_file"
    mode="$(stat -f '%Lp' "$arm_file")"
    chmod "$mode" "$out_file"
    merged=$((merged + 1))
    continue
  fi

  if [[ $arm_exists -eq 1 && $amd_exists -eq 1 ]]; then
    if ! cmp -s "$arm_file" "$amd_file"; then
      case "$rel" in
        ./Contents/Info.plist|./Contents/Resources/qt.conf)
          echo "warning: non-Mach-O metadata differs across slices; preferring arm64 copy: $rel" >&2
          warnings=$((warnings + 1))
          ;;
      esac
    fi
  fi
  skipped=$((skipped + 1))
done < "$tmpdir/all-files.txt"

/usr/libexec/PlistBuddy -c 'Set :LSMinimumSystemVersion 15.0' "$out_app/Contents/Info.plist"

codesign --force --deep --sign - "$out_app"
codesign --verify --deep --verbose=2 "$out_app"

echo "merged Mach-O files: $merged"
echo "skipped non-Mach-O files: $skipped"
echo "warnings: $warnings"
