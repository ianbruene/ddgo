# Releasing desktop artifacts

This project ships GUI desktop artifacts built with `-tags 'miqt serial'`.

## Release policy

- Minimum supported macOS: `15.0`
- macOS release artifact: `DDGo-macos-universal.dmg`
- macOS architectures: `arm64` + `x86_64`
- Windows release artifact: `DDGo-windows-amd64.zip`

macOS 14 and earlier are no longer supported.

## Artifacts

- `DDGo-macos-universal.dmg`
- `DDGo-windows-amd64.zip`

## GitHub Actions runner policy

Use explicit macOS runner labels for release builds:

- Apple Silicon slice: `macos-15`
- Intel slice: `macos-15-intel`

Do not use `macos-14`, `macos-14-large`, `macos-latest`, or `macos-latest-large` for release artifacts.

## macOS universal build flow

The universal macOS app is built by packaging separate `arm64` and `x86_64` app bundles, then merging every Mach-O file with `lipo`. It is not sufficient to merge only `Contents/MacOS/ddgo`.

1. Build `dist/macos-arm64/DDGo.app` on `macos-15`.
2. Build `dist/macos-amd64/DDGo.app` on `macos-15-intel`.
3. Merge them with `scripts/merge-macos-universal.sh` into `dist/macos-universal/DDGo.app`.
4. Verify universality and deployment target checks with `scripts/verify-macos-universal-app.sh dist/macos-universal/DDGo.app 15.0`.
5. Build the final DMG with `scripts/create-macos-dmg.sh dist/macos-universal/DDGo.app dist/DDGo-macos-universal.dmg`.

`package-macos.sh` sets `CFBundleExecutable=ddgo`, sets `LSMinimumSystemVersion` in `Info.plist`, bundles Qt frameworks/plugins with `macdeployqt`, and signs the app.

## Windows (MSYS2 / MinGW)

1. Install packages in an MSYS2 MINGW64 shell:
   `pacman -S --needed mingw-w64-x86_64-go mingw-w64-x86_64-gcc mingw-w64-x86_64-qt5-base mingw-w64-x86_64-pkgconf zip`
2. Build executable in that shell:
   `GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -tags 'miqt serial' -ldflags '-s -w -H windowsgui' -o dist/ddgo-windows-amd64.exe ./cmd/ddgo`
3. Package zip with runtime DLLs:
   `scripts/package-windows-msys2.sh dist/ddgo-windows-amd64.exe dist/DDGo-windows-amd64.zip`
4. Verify distribution layout after extraction:
   `pwsh -File scripts/verify-windows-dist.ps1 -DistDir extracted/DDGo`

The zip contains a `DDGo/` folder. Testers should extract the whole zip and run `DDGo/DDGo.exe`; moving the exe out of that folder will break Qt DLL/plugin loading.
